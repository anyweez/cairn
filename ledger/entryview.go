package ledger

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// EntryView keeps a read model of a stream's history in step with its
// entries, in the transaction that records them: a timeline, an audit trail,
// anything with a row per change. A View can't hold one, since it is written
// once per transaction from the stream's state, and a transaction can record
// several of a stream's messages.
//
// Write is called once for each recorded entry that has events, in order,
// with the stream's state before and after it and the entry's events. They
// are the events Decide returned, or when rebuilding, the stored ones decoded;
// the two are equal for any event type that survives its codec (D6).
// Refusals, duplicates and entries without events don't reach it. Key its
// rows by the entry (after.Stream.ID and entry.Seq, plus an index for an
// entry that writes several) and insert with ON CONFLICT, so that running it
// again changes nothing: RebuildEntries replays every entry through Write.
//
// entry.Position and entry.TxID are zero, as for a View: the entry is written
// in the same round trip. Read who and when from entry.Meta and
// entry.RecordedAt.
//
// Write queues its statements in b, as View.Write does, and they are sent with
// the message's own writes and its commit, after the stream's views, so its
// rows may reference theirs. An error from Write keeps that message from being
// recorded; an error from a statement it queued keeps the transaction from
// committing.
type EntryView[S, Ev any] struct {
	Name  string
	Write func(b *Batch, before, after Loaded[S], entry Entry, events []Ev) error
}

// queueEntryViews queues every entry view's writes for one entry with events.
func (a *Aggregate[S, M, R, Ev, Ef]) queueEntryViews(b *Batch, before, after Loaded[S], entry Entry, events []Ev) error {
	entry.Position, entry.TxID = 0, 0
	for _, v := range a.EntryViews {
		if err := queueEntryView(b, v, before, after, entry, events); err != nil {
			return err
		}
	}
	return nil
}

func queueEntryView[S, Ev any](b *Batch, v EntryView[S, Ev], before, after Loaded[S], entry Entry, events []Ev) error {
	b.owner = v.Name
	defer func() { b.owner = "" }()
	if err := v.Write(b, before, after, entry, events); err != nil {
		return &viewFailure{fmt.Errorf("cairn: entry view %s at %s#%d: %w", v.Name, after.Stream, entry.Seq, err)}
	}
	return nil
}

// RebuildEntries replays every entry with events of every stream of the
// aggregate's kind through v, returning how many streams it replayed. Use it
// when an entry view is added or its rows change shape: delete the rows it
// replaces first, or make Write overwrite them. State is folded from the first
// entry, not loaded from a snapshot. Each stream is locked and replayed in its
// own transaction, so RebuildEntries can run while the aggregate is taking
// messages.
func (a *Aggregate[S, M, R, Ev, Ef]) RebuildEntries(ctx context.Context, v EntryView[S, Ev]) (int, error) {
	rows, err := a.Store.pool.Query(ctx, `
SELECT DISTINCT stream FROM cairn.entries
WHERE kind = $1 AND events <> '[]'::jsonb ORDER BY stream`, a.Kind)
	if err != nil {
		return 0, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for i, name := range names {
		id := strings.TrimPrefix(name, a.Kind+"/")
		err := a.Store.Transact(ctx, func(tx *Tx) error {
			if err := tx.Lock(ctx, a.Stream(id)); err != nil {
				return err
			}
			entries, err := readStream(ctx, tx, a.Stream(id), 0)
			if err != nil {
				return err
			}
			var b Batch
			at := Loaded[S]{Stream: a.Stream(id), State: a.Decider.Initial()}
			for _, e := range entries {
				events, err := a.Events.DecodeAll(e.Events)
				if err != nil {
					return fmt.Errorf("cairn: %s#%d: %w", name, e.Seq, err)
				}
				next := at
				next.State, next.Seq = a.Decider.Fold(at.State, events), e.Seq
				if len(events) > 0 {
					if err := queueEntryView(&b, v, at, next, e, events); err != nil {
						return err
					}
				}
				at = next
			}
			return viewError(b.send(ctx, tx.Tx), at)
		})
		if err != nil {
			return i, fmt.Errorf("cairn: rebuilding entry view %s at %s: %w", v.Name, name, err)
		}
	}
	return len(names), nil
}
