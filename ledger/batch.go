package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
			j, unbuilt := b.blame(s, i, err)
			// An unbuilt statement is one the client could not encode, so the
			// batch was never sent and nothing can have committed — whatever
			// state the connection happens to be in.
			if !unbuilt && b.commits && b.hasCommit && committed(s, err, closeErr) {
				return &recordedError{err: b.failed(j, err)}
			}
			return b.failed(j, err)
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

// failed reports the statement at i having failed: its owner and which of that
// owner's statements it was. Callers name the owner in their own words.
func (b *Batch) failed(i int, err error) error {
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

// blame returns the index of the queued statement err belongs to, starting
// from the one whose result reported it, and whether the client failed to
// build that statement. A build failure happens before the batch is sent: pgx
// prepares every statement before running any, and encodes every argument
// before sending any, so a statement it could not build never ran.
func (b *Batch) blame(s batchSender, i int, err error) (int, bool) {
	var prep pgx.ErrPreprocessingBatch
	if errors.As(err, &prep) {
		if j, ok := b.blamePrepared(s, prep.SQL()); ok {
			i = j
		}
		return i, true
	}
	// A server error is one statement having failed on the server, after
	// everything was sent: it cannot be an argument the client could not
	// encode.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return i, false
	}
	if j, ok := b.blameUnbuilt(s, err); ok {
		return j, true
	}
	return i, false
}

// blamePrepared returns the index of the queued statement with the given SQL
// that pgx could not build, so that a build failure names the statement whose
// arguments are bad rather than the first one that happens to share its SQL.
// pgx prepares every statement before it builds any argument, so the
// statements before the failing one — including any with the same SQL — built
// fine, and it stops at the first whose arguments it cannot encode.
//
// The type map encodes each argument without knowing the types the statement
// declared for its parameters, which pgx does not report with the error: it
// infers a type from each value and accepts either format, as pgx does. A
// value that encodes as its own type but not as the parameter's (a string
// where the statement expects an integer) is therefore not recognised, and
// neither is a failure of the SQL itself rather than of an argument, which
// every statement with that SQL shares. Then the first match stands, as it did
// before.
func (b *Batch) blamePrepared(s batchSender, sql string) (int, bool) {
	m := senderTypeMap(s)
	if m == nil {
		return 0, false
	}
	first := -1
	for j, q := range b.queued.QueuedQueries {
		if q.SQL != sql {
			continue
		}
		if first < 0 {
			first = j
		}
		if !encodable(m, q.Arguments) {
			return j, true
		}
	}
	return first, first >= 0
}

// blameUnbuilt returns the index of the queued statement whose arguments the
// client could not encode, naming the owner of a batch that failed before it
// was sent. The simple protocol has pgx turn each statement's parameters into
// SQL text as it builds the batch, which it does without preparing or
// describing anything: the failure is a plain encoding error, not a
// pgx.ErrPreprocessingBatch, and it names neither the statement nor its SQL.
// The same statement is found again here, and it is blamed only when encoding
// its arguments gives the very error the batch reported. A client-side failure
// that is not an encoding one — a QueryRewriter's, say, or a connection that
// cannot run the simple protocol at all — keeps the behaviour it had.
func (b *Batch) blameUnbuilt(s batchSender, err error) (int, bool) {
	m := senderTypeMap(s)
	if m == nil {
		return 0, false
	}
	for j, q := range b.queued.QueuedQueries {
		if e := unencoded(m, q.Arguments); e != nil && reported(err, e) {
			return j, true
		}
	}
	return 0, false
}

// reported reports whether err, or one of the errors it wraps, says what e
// says. pgx builds a fresh error for the same argument each time, so the text
// is the only thing that ties the reported failure to the statement that
// caused it.
func reported(err, e error) bool {
	for ; err != nil; err = errors.Unwrap(err) {
		if err.Error() == e.Error() {
			return true
		}
	}
	return false
}

// unencoded returns the error from encoding the first argument pgx cannot
// encode, as the simple protocol does — as text, through the argument's own
// type — or nil when every argument encodes.
func unencoded(m *pgtype.Map, args []any) error {
	for _, arg := range arguments(args) {
		if _, err := m.Encode(0, pgtype.TextFormatCode, arg, []byte{}); err != nil {
			return err
		}
	}
	return nil
}

// encodable reports whether pgx could build every argument of a statement. An
// argument that either format encodes is one pgx would accept.
func encodable(m *pgtype.Map, args []any) bool {
	for _, arg := range arguments(args) {
		if _, err := m.Encode(0, pgtype.BinaryFormatCode, arg, nil); err == nil {
			continue
		}
		if _, err := m.Encode(0, pgtype.TextFormatCode, arg, nil); err == nil {
			continue
		}
		return false
	}
	return true
}

// arguments drops the leading QueryRewriters: pgx applies one instead of
// encoding it as an argument.
func arguments(args []any) []any {
	for len(args) > 0 {
		if _, ok := args[0].(pgx.QueryRewriter); !ok {
			break
		}
		args = args[1:]
	}
	return args
}

// senderTypeMap is the type map of the connection a batch was sent on, or nil
// for a sender that doesn't name one.
func senderTypeMap(s batchSender) *pgtype.Map {
	switch s := s.(type) {
	case *Tx:
		return s.tx.Conn().TypeMap()
	case interface{ Conn() *pgx.Conn }:
		return s.Conn().TypeMap()
	}
	return nil
}
