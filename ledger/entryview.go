package ledger

import (
	"context"
	"fmt"

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
// entry.Position and entry.TxID are zero, as for a View, rebuilding included:
// the entry is written in the same round trip. Read who and when from
// entry.Meta and entry.RecordedAt.
//
// Write may run more than once for one entry before it is recorded: a group
// that fails is decided again, from stored state or one message at a time.
// Only the statements queued by the attempt that commits take effect, so
// Write must do nothing but queue them. It must not change the states it is
// given, which the store's state cache may still hold.
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
	for _, v := range a.EntryViews {
		if err := queueEntryView(b, v, before, after, entry, events); err != nil {
			return err
		}
	}
	return nil
}

// queueEntryView queues one entry view's writes. Position and TxID are zeroed
// on every path, rebuilding included, so a rebuilt row is the row a live
// write made.
func queueEntryView[S, Ev any](b *Batch, v EntryView[S, Ev], before, after Loaded[S], entry Entry, events []Ev) error {
	entry.Position, entry.TxID = 0, 0
	if err := b.as(v.Name, func() error { return v.Write(b, before, after, entry, events) }); err != nil {
		return &viewFailure{fmt.Errorf("cairn: entry view %s at %s#%d: %w", v.Name, after.Stream, entry.Seq, err)}
	}
	return nil
}

// rebuildWindow is how many of a stream's entries RebuildEntries replays in
// one transaction.
var rebuildWindow = 1000

// RebuildEntries replays every entry with events of every stream of the
// aggregate's kind through v, returning how many streams it replayed. Use it
// when an entry view is added or its rows change shape: delete the rows it
// replaces first, or make Write overwrite them. State is folded from the first
// entry, not loaded from a snapshot.
//
// A stream is replayed in windows of entries, each sent and committed in a
// transaction of its own, and no stream is locked, so RebuildEntries can run
// while the aggregate is taking messages: an entry recorded meanwhile is
// written live and again by the replay, which changes nothing, since Write is
// safe to run again. A failed rebuild leaves the windows before the failure
// written; running it again redoes them.
func (a *Aggregate[S, M, R, Ev, Ef]) RebuildEntries(ctx context.Context, v EntryView[S, Ev]) (int, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
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
		stream := parseStream(name, a.Kind)
		if err := a.replayEntries(ctx, stream, v); err != nil {
			return i, fmt.Errorf("cairn: rebuilding entry view %s at %s: %w", v.Name, name, err)
		}
	}
	return len(names), nil
}

// replayEntries replays one stream's entries through v, a window at a time.
func (a *Aggregate[S, M, R, Ev, Ef]) replayEntries(ctx context.Context, stream Stream, v EntryView[S, Ev]) error {
	at := Loaded[S]{Stream: stream, State: a.Decider.Initial()}
	for {
		var n int
		err := a.Store.Transact(ctx, nil, func(tx *Tx) error {
			rows, err := tx.Query(ctx, `SELECT `+entryColumns+` FROM cairn.entries
WHERE stream = $1 AND seq > $2 ORDER BY seq LIMIT $3`, stream.String(), at.Seq, rebuildWindow)
			if err != nil {
				return err
			}
			entries, err := scanEntries(rows)
			if err != nil {
				return err
			}
			n = len(entries)
			var b Batch
			next := at
			for _, e := range entries {
				events, err := a.Codecs.Events.DecodeAll(e.Events)
				if err != nil {
					return fmt.Errorf("cairn: %s#%d: %w", stream, e.Seq, err)
				}
				before := next
				next.State, next.Seq = a.Decider.Fold(next.State, events), e.Seq
				if len(events) > 0 {
					if err := queueEntryView(&b, v, before, next, e, events); err != nil {
						return err
					}
				}
			}
			if err := viewError(b.send(ctx, tx.tx), next); err != nil {
				return err
			}
			at = next
			return nil
		})
		if err != nil || n < rebuildWindow {
			return err
		}
	}
}
