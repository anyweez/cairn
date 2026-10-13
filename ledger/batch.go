package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Batch collects statements that callers hand to the ledger. The ledger sends
// them together with the rest of the transaction's writes, so all of them cost
// one round trip instead of one each. Statements run in the order they were
// queued, for their effect alone: rows they return are discarded. One that
// fails aborts the transaction, with whatever else it was going to record.
//
// Callbacks that write to the database take a Batch rather than a
// transaction; see docs/CONVENTIONS.md.
type Batch struct {
	queued pgx.Batch
	owners []string
	owner  string
}

// Queue adds a statement to the batch.
func (b *Batch) Queue(sql string, args ...any) {
	b.queued.Queue(sql, args...)
	b.owners = append(b.owners, b.owner)
}

// Len is how many statements are queued.
func (b *Batch) Len() int { return b.queued.Len() }

// batchError is a queued statement that failed: the index-th that its owner
// queued. Callers name the owner in their own words.
type batchError struct {
	owner string
	index int
	err   error
}

func (e *batchError) Error() string { return fmt.Sprintf("statement %d: %v", e.index, e.err) }

func (e *batchError) Unwrap() error { return e.err }

// send runs every queued statement in tx in one round trip.
func (b *Batch) send(ctx context.Context, tx pgx.Tx) error {
	if b.Len() == 0 {
		return nil
	}
	results := tx.SendBatch(ctx, &b.queued)
	for i := range b.owners {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return b.failed(i, err)
		}
	}
	return results.Close()
}

// failed blames the statement at i for err, unless err comes from preparing
// the batch: pgx prepares every statement before running any, and reports a
// failure there against the first, naming the SQL that actually failed.
func (b *Batch) failed(i int, err error) *batchError {
	var prep pgx.ErrPreprocessingBatch
	if errors.As(err, &prep) {
		for j, q := range b.queued.QueuedQueries {
			if q.SQL == prep.SQL() {
				i = j
				break
			}
		}
	}
	index := 0
	for j := i - 1; j >= 0 && b.owners[j] == b.owners[i]; j-- {
		index++
	}
	return &batchError{owner: b.owners[i], index: index, err: err}
}
