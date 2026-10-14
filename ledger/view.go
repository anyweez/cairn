package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// View keeps a read model in step with each stream's state, in the
// transaction that changes it. A caller that writes and then reads the read
// model sees its own write, which a Projection over the global log cannot
// promise.
//
// Write is given the stream's whole state, not the events that changed it: it
// replaces the stream's rows rather than adjusting them. That is what makes a
// view safe to run again, and lets Rebuild fill a new or changed view from
// current state with no checkpoint. A read model that combines streams
// (counts, totals) needs a Projection.
//
// A view is written once per transaction, as of the transaction's last entry
// with events, and a transaction can record several of a stream's messages.
// So a view can't keep a row per entry; a history (a timeline, an audit trail)
// needs an EntryView.
//
// cause is the stream's latest entry with events, the one that produced at.
// Read who changed the stream and when from it (cause.Meta.Actor,
// cause.RecordedAt) for display columns like updated_by. A fact a decision
// depends on belongs in the state instead. cause.Position and cause.TxID are
// zero: the entry is written in the same round trip as the view, so they are
// not known yet.
//
// Write queues its statements in b instead of running them. They are sent with
// the message's own writes and its commit, in one round trip, while the
// stream is locked; a view that ran its own would add a round trip per
// statement to that wait. Everything a view needs is in at and cause, so it
// has nothing to read first. An error from Write, or from any statement it
// queued, keeps the message from being recorded.
type View[S any] struct {
	Name  string
	Write func(b *Batch, at Loaded[S], cause Entry) error
}

// queueViews queues every view's writes after a message that produced events.
// A refusal, a duplicate or an accepted message with no events changes no
// state, so it writes nothing; Rebuild skips the same streams.
func (a *Aggregate[S, M, R, Ev, Ef]) queueViews(b *Batch, at Loaded[S], cause Entry) error {
	for _, v := range a.Views {
		if err := queueView(b, v, at, cause); err != nil {
			return err
		}
	}
	return nil
}

func queueView[S any](b *Batch, v View[S], at Loaded[S], cause Entry) error {
	cause.Position, cause.TxID = 0, 0
	b.owner = v.Name
	defer func() { b.owner = "" }()
	if err := v.Write(b, at, cause); err != nil {
		return &viewFailure{fmt.Errorf("cairn: view %s at %s#%d: %w", v.Name, at.Stream, at.Seq, err)}
	}
	return nil
}

// viewFailure is a view's Write returning an error. Nothing was sent, so the
// messages it was writing for can be tried again one at a time.
type viewFailure struct{ error }

func (e *viewFailure) Unwrap() error { return e.error }

// viewError names the view that queued a failing statement. Errors from the
// ledger's own statements already say what failed.
func viewError[S any](err error, at Loaded[S]) error {
	var failed *batchError
	if errors.As(err, &failed) {
		return fmt.Errorf("cairn: view %s at %s#%d: %w", failed.owner, at.Stream, at.Seq, failed)
	}
	return err
}

// Rebuild writes v for every stream of the aggregate's kind that has recorded
// an event, returning how many it wrote. Use it when a view is added or its
// rows change shape. Each stream is locked and written in its own
// transaction, so Rebuild can run while the aggregate is taking messages.
func (a *Aggregate[S, M, R, Ev, Ef]) Rebuild(ctx context.Context, v View[S]) (int, error) {
	if err := a.Validate(); err != nil {
		return 0, err
	}
	// A stream with no events has nothing in any view.
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
		err := a.Store.Transact(ctx, []Stream{stream}, func(tx *Tx) error {
			at, err := a.LoadLocked(ctx, tx, stream.ID)
			if err != nil {
				return err
			}
			// The same entry a live write would have passed: the latest with events.
			rows, err := tx.Query(ctx, `SELECT `+entryColumns+` FROM cairn.entries
WHERE stream = $1 AND events <> '[]'::jsonb ORDER BY seq DESC LIMIT 1`, name)
			if err != nil {
				return err
			}
			cause, err := scanEntries(rows)
			if err != nil {
				return err
			}
			if len(cause) == 0 {
				return fmt.Errorf("cairn: %s has no entry with events", name)
			}
			// Later entries are refusals or carried no events, so the state is
			// the same; the seq is the one a live write saw.
			at.Seq = cause[0].Seq
			var b Batch
			if err := queueView(&b, v, at, cause[0]); err != nil {
				return err
			}
			return viewError(b.send(ctx, tx.Tx), at)
		})
		if err != nil {
			return i, fmt.Errorf("cairn: rebuilding view %s at %s: %w", v.Name, name, err)
		}
	}
	return len(names), nil
}
