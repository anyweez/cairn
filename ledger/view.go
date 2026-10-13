package ledger

import (
	"context"
	"fmt"
	"strings"

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
// cause is the stream's latest entry with events, the one that produced at.
// Read who changed the stream and when from it (cause.Meta.Actor,
// cause.RecordedAt) for display columns like updated_by. A fact a decision
// depends on belongs in the state instead.
type View[S any] struct {
	Name  string
	Write func(ctx context.Context, tx pgx.Tx, at Loaded[S], cause Entry) error
}

// writeViews runs every view after a message that produced events. A refusal,
// a duplicate or an accepted message with no events changes no state, so it
// writes nothing; Rebuild skips the same streams.
func (a *Aggregate[S, M, R, Ev, Ef]) writeViews(ctx context.Context, tx *Tx, at *Loaded[S], cause Entry) error {
	for _, v := range a.Views {
		if err := v.Write(ctx, tx.Tx, *at, cause); err != nil {
			return fmt.Errorf("cairn: view %s at %s#%d: %w", v.Name, at.Stream, at.Seq, err)
		}
	}
	return nil
}

// Rebuild writes v for every stream of the aggregate's kind that has recorded
// an event, returning how many it wrote. Use it when a view is added or its
// rows change shape. Each stream is locked and written in its own
// transaction, so Rebuild can run while the aggregate is taking messages.
func (a *Aggregate[S, M, R, Ev, Ef]) Rebuild(ctx context.Context, v View[S]) (int, error) {
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
		id := strings.TrimPrefix(name, a.Kind+"/")
		err := a.Store.Transact(ctx, func(tx *Tx) error {
			if err := tx.Lock(ctx, a.Stream(id)); err != nil {
				return err
			}
			at, err := a.LoadLocked(ctx, tx, id)
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
			return v.Write(ctx, tx.Tx, at, cause[0])
		})
		if err != nil {
			return i, fmt.Errorf("cairn: rebuilding view %s at %s: %w", v.Name, name, err)
		}
	}
	return len(names), nil
}
