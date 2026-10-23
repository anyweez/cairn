package ledger

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Projection folds the global log into something else — a read model table,
// a search index — keeping its place in a named checkpoint.
//
// Apply queues its writes to this database in b instead of running them. A
// batch of entries' statements is sent together with the checkpoint's advance,
// in one round trip, and commits with it, so a read model kept here sees each
// entry exactly once. A failing statement rolls the batch back and leaves the
// checkpoint where it was. A read model that lives elsewhere is written from
// Apply directly, using ctx. It sees each entry at least once, and must be
// idempotent.
type Projection struct {
	Store *Store
	// Name keys the projection's checkpoint, so it is required: a projection
	// with no Name would share the empty checkpoint with every other unnamed
	// projection, and only the first to run would apply anything.
	Name string
	// Kinds limits the projection to these stream kinds. Empty means all.
	Kinds []string
	Apply func(ctx context.Context, b *Batch, e Entry) error
	// BatchSize is how many entries one transaction applies. Defaults to 100.
	BatchSize int
	// OnError, when set, keeps Run going after an error, reporting it here and
	// trying again at the next interval. When nil, Run returns the error.
	OnError func(error)
}

// Validate reports the first thing wrong with how the projection is set up: no
// store, no Name, a Name the ledger cannot store or index, or no Apply
// function. The name keys its checkpoint, so an unnamed projection has no place
// of its own in the log. CatchUp runs it first, so call it at startup to fail
// before Run would.
func (p *Projection) Validate() error {
	fail := func(what string) error { return fmt.Errorf("cairn: projection %q: %s", p.Name, what) }
	switch {
	case p.Store == nil:
		return fail("Store is nil")
	case p.Name == "":
		return fail("Name must be non-empty; it keys the checkpoint, and unnamed projections would share one")
	case p.Apply == nil:
		return fail("Apply is nil")
	}
	// Name is the primary key of cairn.checkpoints, so it is bounded like every
	// other text value cairn puts in a btree index: a longer one, or one with a
	// NUL, otherwise reaches Postgres and fails every CatchUp with a raw error
	// about an index row.
	return validIndexedText("the Name of projection "+quoteText(p.Name), p.Name)
}

// CatchUp applies every entry visible now, returning how many it applied.
func (p *Projection) CatchUp(ctx context.Context) (int, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	batch := p.BatchSize
	if batch <= 0 {
		batch = 100
	}
	total := 0
	for {
		n := 0
		// READ COMMITTED is required, not the default: under REPEATABLE READ the
		// transaction's snapshot would be taken here, before readAll measures the
		// log fence, and a transaction committing in between would be invisible
		// to the read while the fence had already passed it. The fence has to be
		// the batch's first pg_stat_activity read, so the isolation is set on the
		// BEGIN, not with a SET inside it.
		err := pgx.BeginTxFunc(ctx, p.Store.pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
			// Read the batch before touching the checkpoint row: locking it gives
			// this transaction an ID, and the log only shows entries older than
			// every running transaction.
			var after Cursor
			var txid string
			err := tx.QueryRow(ctx, `SELECT txid::text, position FROM cairn.checkpoints WHERE name = $1`, p.Name).
				Scan(&txid, &after.Position)
			switch {
			case err == pgx.ErrNoRows:
			case err != nil:
				return fmt.Errorf("cairn: reading checkpoint %s: %w", p.Name, err)
			default:
				if after.TxID, err = strconv.ParseUint(txid, 10, 64); err != nil {
					return err
				}
			}

			entries, err := readAll(ctx, tx, after, batch, p.Kinds)
			if err != nil || len(entries) == 0 {
				return err
			}

			// Claim the checkpoint, first in the batch. Another runner of the same
			// projection that got here first has moved it; give up this batch,
			// and roll back what the rest of it wrote, rather than apply twice.
			var b Batch
			b.own(func(r pgx.BatchResults) error {
				tag, err := r.Exec()
				switch {
				case err != nil:
					return fmt.Errorf("cairn: advancing checkpoint %s: %w", p.Name, err)
				case tag.RowsAffected() == 0:
					return errLostCheckpoint
				}
				return nil
			}, `
INSERT INTO cairn.checkpoints (name, txid, position) VALUES ($1, $2::text::xid8, $3)
ON CONFLICT (name) DO UPDATE SET txid = excluded.txid, position = excluded.position, updated_at = now()
WHERE cairn.checkpoints.txid = $4::text::xid8 AND cairn.checkpoints.position = $5`,
				p.Name, strconv.FormatUint(entries[len(entries)-1].TxID, 10), entries[len(entries)-1].Position,
				strconv.FormatUint(after.TxID, 10), after.Position)

			for _, e := range entries {
				b.owner = fmt.Sprintf("%s#%d", e.Stream, e.Seq)
				if err := p.Apply(ctx, &b, e); err != nil {
					return fmt.Errorf("cairn: projection %s at %s: %w", p.Name, b.owner, err)
				}
			}
			err = b.send(ctx, tx)
			if failed := (*batchError)(nil); errors.As(err, &failed) {
				return fmt.Errorf("cairn: projection %s at %s: %w", p.Name, failed.owner, failed)
			}
			if err != nil {
				return err
			}
			n = len(entries)
			return nil
		})
		if err == errLostCheckpoint {
			continue
		}
		if err != nil {
			return total, err
		}
		// A short batch means there are no more entries visible as of the read
		// that produced it. Return without reading again: the next call picks up
		// anything that commits meanwhile, and the terminating transaction would
		// otherwise cost four round trips on its own.
		if n < batch {
			return total + n, nil
		}
		total += n
	}
}

var errLostCheckpoint = fmt.Errorf("cairn: checkpoint moved underneath this runner")

// Run catches the projection up every interval until ctx is done. An error
// ends it unless OnError is set. The interval must be positive.
func (p *Projection) Run(ctx context.Context, interval time.Duration) error {
	if err := p.Validate(); err != nil {
		return err
	}
	return poll(ctx, interval, p.OnError, func(ctx context.Context) (bool, error) {
		// CatchUp applies everything visible, so there is nothing to repeat.
		_, err := p.CatchUp(ctx)
		return false, err
	})
}

// poll calls once every interval until ctx is done, and again at once while
// once reports more to do. An error ends it unless onError is set, in which
// case it is reported there and the next interval tries again.
func poll(ctx context.Context, interval time.Duration, onError func(error), once func(context.Context) (bool, error)) error {
	if interval <= 0 {
		return fmt.Errorf("cairn: interval must be positive, got %v", interval)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			again, err := once(ctx)
			if err != nil && ctx.Err() == nil {
				if onError == nil {
					return err
				}
				onError(err)
			}
			if err != nil || !again {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
