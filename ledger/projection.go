package ledger

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Projection folds the global log into something else — a read model table,
// a search index — keeping its place in a named checkpoint.
//
// Apply runs in the same transaction that advances the checkpoint, so a read
// model kept in this database sees each entry exactly once. One that lives
// elsewhere sees each entry at least once, and must be idempotent.
type Projection struct {
	Name string
	// Kinds limits the projection to these stream kinds. Empty means all.
	Kinds []string
	Apply func(ctx context.Context, tx pgx.Tx, e Entry) error
	// BatchSize is how many entries one transaction applies. Defaults to 100.
	BatchSize int
	// OnError, when set, keeps Run going after an error, reporting it here and
	// trying again at the next interval. When nil, Run returns the error.
	OnError func(error)
}

// CatchUp applies every entry visible now, returning how many it applied.
func (s *Store) CatchUp(ctx context.Context, p Projection) (int, error) {
	batch := p.BatchSize
	if batch <= 0 {
		batch = 100
	}
	total := 0
	for {
		n := 0
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
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

			// Claim the checkpoint. Another runner of the same projection that got
			// here first has moved it; give up this batch rather than apply twice.
			tag, err := tx.Exec(ctx, `
INSERT INTO cairn.checkpoints (name, txid, position) VALUES ($1, $2::text::xid8, $3)
ON CONFLICT (name) DO UPDATE SET txid = excluded.txid, position = excluded.position, updated_at = now()
WHERE cairn.checkpoints.txid = $4::text::xid8 AND cairn.checkpoints.position = $5`,
				p.Name, strconv.FormatUint(entries[len(entries)-1].TxID, 10), entries[len(entries)-1].Position,
				strconv.FormatUint(after.TxID, 10), after.Position)
			if err != nil {
				return fmt.Errorf("cairn: advancing checkpoint %s: %w", p.Name, err)
			}
			if tag.RowsAffected() == 0 {
				return errLostCheckpoint
			}

			for _, e := range entries {
				if err := p.Apply(ctx, tx, e); err != nil {
					return fmt.Errorf("cairn: projection %s at %s#%d: %w", p.Name, e.Stream, e.Seq, err)
				}
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
		if n == 0 {
			return total, nil
		}
		total += n
	}
}

var errLostCheckpoint = fmt.Errorf("cairn: checkpoint moved underneath this runner")

// Run catches the projection up every interval until ctx is done. An error
// ends it unless the projection sets OnError.
func (s *Store) Run(ctx context.Context, p Projection, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.CatchUp(ctx, p); err != nil && ctx.Err() == nil {
			if p.OnError == nil {
				return err
			}
			p.OnError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
