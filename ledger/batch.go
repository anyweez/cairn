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
	items  []batchItem
	owner  string
}

// batchItem is one queued statement: who queued it, empty for the ledger's
// own, and how its result is read.
type batchItem struct {
	owner  string
	handle func(pgx.BatchResults) error
}

// Queue adds a statement to the batch.
func (b *Batch) Queue(sql string, args ...any) {
	b.queued.Queue(sql, args...)
	b.items = append(b.items, batchItem{owner: b.owner, handle: execResult})
}

// Len is how many statements are queued.
func (b *Batch) Len() int { return b.queued.Len() }

// own adds one of the ledger's own statements. handle reads its result, after
// every statement before it has been read, and an error from handle is
// returned as it is: handle wraps its own.
func (b *Batch) own(handle func(pgx.BatchResults) error, sql string, args ...any) {
	b.queued.Queue(sql, args...)
	b.items = append(b.items, batchItem{handle: handle})
}

func execResult(r pgx.BatchResults) error {
	_, err := r.Exec()
	return err
}

// batchError is a queued statement that failed: the index-th that its owner
// queued. Callers name the owner in their own words.
type batchError struct {
	owner string
	index int
	err   error
}

func (e *batchError) Error() string { return fmt.Sprintf("statement %d: %v", e.index, e.err) }

func (e *batchError) Unwrap() error { return e.err }

// batchSender is a transaction or a connection.
type batchSender interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// send runs every queued statement in one round trip and reads each result in
// order. It stops at the first that fails, returning a *batchError when a
// caller queued it.
func (b *Batch) send(ctx context.Context, s batchSender) error {
	if b.Len() == 0 {
		return nil
	}
	results := s.SendBatch(ctx, &b.queued)
	for i, item := range b.items {
		if err := item.handle(results); err != nil {
			_ = results.Close()
			return b.failed(i, err)
		}
	}
	return results.Close()
}

// failed blames the statement at i for err, unless err comes from preparing
// the batch: pgx prepares every statement before running any, and reports a
// failure there against the first, naming the SQL that actually failed.
func (b *Batch) failed(i int, err error) error {
	var prep pgx.ErrPreprocessingBatch
	if errors.As(err, &prep) {
		for j, q := range b.queued.QueuedQueries {
			if q.SQL == prep.SQL() {
				i = j
				break
			}
		}
	}
	owner := b.items[i].owner
	if owner == "" {
		return err
	}
	index := 0
	for j := i - 1; j >= 0 && b.items[j].owner == owner; j-- {
		index++
	}
	return &batchError{owner: owner, index: index, err: err}
}
