package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	// commits is set on a batch that ends its own transaction and may have
	// recorded a message. send can report ErrRecorded for it only when
	// hasCommit proves the transaction committed.
	commits bool
	// hasCommit records that the batch queues its own COMMIT. A batch that
	// leaves the commit to the connection runs as one implicit transaction: a
	// later statement's server-side error rolls the whole thing back and the
	// connection still reads idle, the same as after a commit, so send must
	// never claim the message was recorded for it.
	hasCommit bool
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

// merge moves every statement queued on other to the end of b.
func (b *Batch) merge(other *Batch) {
	b.queued.QueuedQueries = append(b.queued.QueuedQueries, other.queued.QueuedQueries...)
	b.items = append(b.items, other.items...)
	*other = Batch{}
}

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
			// Postgres runs every statement before the caller reads any result,
			// so a handler can fail in Go — a scan or a parse — with the
			// statements already done. A batch that ends its own transaction
			// may have committed one the caller was never told about; read the
			// rest, and say so rather than reporting a failure that did not
			// happen.
			closeErr := results.Close()
			if b.commits && b.hasCommit && committed(s, err, closeErr) {
				return &recordedError{err: b.failed(i, err)}
			}
			return b.failed(i, err)
		}
	}
	return results.Close()
}

// ErrRecorded reports that a batch's transaction committed, but a result could
// not be read from it: the statements ran, the message is recorded, and the
// error only says the caller cannot be told what it produced. It is wrapped by
// the error naming what failed to read. A caller that sees it must not replay
// the message unless it carries an idempotency key, because a replay records
// it a second time.
var ErrRecorded = errors.New("cairn: the message was recorded, but its result could not be read")

// recordedError is a handler failing to read a result from a batch the
// database had already committed. It carries the error that says what could
// not be read, and matches ErrRecorded.
type recordedError struct{ err error }

func (e *recordedError) Error() string { return fmt.Sprintf("%v: %v", ErrRecorded, e.err) }

func (e *recordedError) Unwrap() error { return e.err }

func (e *recordedError) Is(target error) bool { return target == ErrRecorded }

// committed reports whether s's connection is between transactions after a
// batch's results have been read: 'I' is idle, which is what a committed
// transaction leaves behind, 'T' or 'E' that it is still open or aborted, and
// a closed connection leaves it unknown. A database error aborts the
// transaction, so it is never a commit. s's own result-reading may report a
// client-side error the database knows nothing about, which is exactly the
// case this is here to see past.
func committed(s batchSender, errs ...error) bool {
	for _, err := range errs {
		var pgErr *pgconn.PgError
		var prep pgx.ErrPreprocessingBatch
		if errors.As(err, &pgErr) || errors.As(err, &prep) {
			return false
		}
	}
	pg := senderPgConn(s)
	return pg != nil && !pg.IsClosed() && pg.TxStatus() == 'I'
}

// senderPgConn is the server connection a batch was sent on, or nil for a
// sender that doesn't name one.
func senderPgConn(s batchSender) *pgconn.PgConn {
	switch s := s.(type) {
	case *Tx:
		return s.tx.Conn().PgConn()
	case interface{ Conn() *pgx.Conn }:
		return s.Conn().PgConn()
	}
	return nil
}

// end queues the statement that ends the batch's transaction — a COMMIT — and
// marks the batch as one whose outcome send may report. Only an explicit
// COMMIT makes that outcome readable: the connection reads idle whether an
// implicit transaction committed or a later statement rolled it back.
func (b *Batch) end() {
	b.own(commitStep, `COMMIT`)
	b.commits = true
	b.hasCommit = true
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
