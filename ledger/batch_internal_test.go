package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scanOneInt reads one integer, so a query that returns text fails in Go with
// the server none the wiser.
func scanOneInt(r pgx.BatchResults) error {
	var n int
	return r.QueryRow().Scan(&n)
}

// insertOne queues an insert and reads its command tag, the way the ledger's
// own statements do.
func insertOne(r pgx.BatchResults) error {
	_, err := r.Exec()
	return err
}

// committedConn gives a fresh connection with a temporary table to insert
// into, and a count of its rows.
func committedConn(t *testing.T) (context.Context, *pgxpool.Conn, func() int) {
	t.Helper()
	ctx := context.Background()
	conn, err := emptyDatabase(t).Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Release)
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE committed (n int)`); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		// A batch that aborted an explicit transaction leaves it open, as
		// release rolls it back in production.
		if pg := conn.Conn().PgConn(); !pg.IsClosed() && pg.TxStatus() != 'I' {
			if _, err := conn.Exec(ctx, `ROLLBACK`); err != nil {
				t.Fatal(err)
			}
		}
		var rows int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM committed`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	return ctx, conn, count
}

// as scopes the owner to the statements its callback queues, restoring the
// previous owner when the callback returns, its error included. The ledger's
// own statements queued after it are left with no owner.
func TestAsScopesTheOwnerToItsCallback(t *testing.T) {
	t.Parallel()
	var b Batch
	if err := b.as("outer", func() error {
		b.Queue(`SELECT 1`)
		return b.as("inner", func() error {
			b.Queue(`SELECT 2`)
			return errors.New("stopped")
		})
	}); err == nil {
		t.Fatal("as returned nil, want the callback's error")
	}
	b.Queue(`SELECT 3`)

	want := []string{"outer", "inner", ""}
	for i, owner := range want {
		if got := b.items[i].owner; got != owner {
			t.Fatalf("statement %d is owned by %q, want %q", i, got, owner)
		}
	}
}

// Postgres runs every statement in a batch before the caller reads any result,
// so a handler can fail in Go — a scan or a parse — after the statements,
// including the COMMIT, have already run. The caller gets an error either way,
// but it must say the message was recorded rather than that the transaction
// failed, or a retry records it a second time.
func TestABatchThatCommittedDespiteAFailedResultSaysSo(t *testing.T) {
	t.Parallel()
	ctx, conn, rows := committedConn(t)

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.own(scanOneInt, `SELECT 'not an integer'`)
	b.end()

	err := b.send(ctx, conn)
	if !errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, want ErrRecorded", err)
	}
	if n := rows(); n != 1 {
		t.Fatalf("the batch's insert is %d rows, want 1: the error does not describe what happened", n)
	}
}

// A batch that ends its own transaction can tell a commit from a rollback: an
// aborted transaction reads 'E', and the message must not be reported as
// recorded. Here the scan fails in Go first, so the database error behind it
// is one the caller never reads.
func TestABatchRefusedByTheDatabaseDoesNotSayItCommitted(t *testing.T) {
	t.Parallel()
	ctx, conn, rows := committedConn(t)

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.own(scanOneInt, `SELECT 'not an integer'`)
	b.own(sqlStep("dividing"), `SELECT 1/0`)
	b.end()

	err := b.send(ctx, conn)
	if err == nil {
		t.Fatal("send returned nil, want an error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, but the transaction was rolled back", err)
	}
	if n := rows(); n != 0 {
		t.Fatalf("the batch's insert is %d rows, want 0: it was reported as recorded", n)
	}
}

// A batch that leaves the commit to the connection runs as one implicit
// transaction. A later statement's database error rolls the whole thing back,
// and the connection reads idle, the same as after a commit, so send must not
// claim the message was recorded. The cached Handle path used to send such a
// batch and used to claim that.
func TestAnImplicitBatchDoesNotSayItCommitted(t *testing.T) {
	t.Parallel()
	ctx, conn, rows := committedConn(t)

	var b Batch
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.own(scanOneInt, `SELECT 'not an integer'`)
	b.own(sqlStep("dividing"), `SELECT 1/0`)
	// The cached path declared its batch committing without queuing a COMMIT.
	b.commits = true

	err := b.send(ctx, conn)
	if err == nil {
		t.Fatal("send returned nil, want an error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, but an implicit transaction cannot be told from a rollback", err)
	}
	if n := rows(); n != 0 {
		t.Fatalf("the batch's insert is %d rows, want 0: it was reported as recorded", n)
	}
}

// A batch sent inside a transaction another statement ends has not committed
// when a handler fails, so the error must not claim it did.
func TestABatchThatDidNotCommitDoesNotSayItDid(t *testing.T) {
	t.Parallel()
	ctx, conn, _ := committedConn(t)

	err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var b Batch
		b.own(scanOneInt, `SELECT 'not an integer'`)
		return b.send(ctx, tx)
	})
	if err == nil {
		t.Fatal("send returned nil, want the scan's error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, but the transaction had not committed", err)
	}
}

// A batch whose context ends after its statements were sent may still have
// committed: all of them, the COMMIT among them, are on their way before any
// result is read. Reporting a plain failure would lose the message if the
// transaction committed, because a caller retries a failure, so the only safe
// answer is that the outcome is unknown — and a cancellation is not, by
// itself, proof that nothing was sent.
func TestAContextThatEndsAfterSendingLeavesTheOutcomeUnknown(t *testing.T) {
	t.Parallel()

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.end()

	err := b.send(context.Background(), contextEndsSender{})
	if err == nil {
		t.Fatal("send returned nil, want the context's error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v; the COMMIT's result was never read, so it cannot say the message was recorded", err)
	}
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("send returned %v, want an error wrapping ErrUnknownOutcome: the context ended after the statements were sent", err)
	}
}

// contextEndsSender answers every statement up to the COMMIT, and fails that
// one with the context ending: the transaction may have committed, and there is
// no connection left to inspect.
type contextEndsSender struct{}

func (contextEndsSender) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	return &contextEndsResults{remaining: b.Len()}
}

type contextEndsResults struct{ remaining int }

func (r *contextEndsResults) Exec() (pgconn.CommandTag, error) {
	r.remaining--
	if r.remaining == 0 {
		return pgconn.CommandTag{}, context.Canceled
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (r *contextEndsResults) Query() (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (r *contextEndsResults) QueryRow() pgx.Row { return nil }

func (r *contextEndsResults) Close() error { return nil }

// An error pgconn marks safe to retry says the statement that reported it never
// reached the server, not that the batch did not. Once a connection has failed
// and been closed, pgconn reports "conn closed", marked safe to retry, for the
// next result it is asked for — which under the simple protocol is the first,
// because the whole batch is one write, the COMMIT included. Taking it as proof
// that nothing committed reports a transaction that may be in the ledger as a
// plain failure, and a caller that retries it records the message twice.
func TestASafeToRetryErrorDoesNotProveTheBatchWasNotSent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		failAt int
	}{
		{"first result", 0},
		{"later result", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var b Batch
			b.own(sqlStep("beginning a transaction"), `BEGIN`)
			b.own(insertOne, `INSERT INTO committed VALUES (1)`)
			b.own(insertOne, `INSERT INTO committed VALUES (2)`)
			b.end()

			err := b.send(context.Background(), closedAfterSender{failAt: tc.failAt})
			if err == nil {
				t.Fatal("send returned nil, want the statement's error")
			}
			if !errors.Is(err, ErrUnknownOutcome) {
				t.Fatalf("send returned %v, want an error wrapping ErrUnknownOutcome: nothing stopped the batch being written, so the COMMIT may have run", err)
			}
		})
	}
}

// closedAfterSender answers every statement before failAt, and fails that one
// with the error pgconn reports once the connection it sent them on has been
// closed: "conn closed", which pgconn marks safe to retry.
type closedAfterSender struct{ failAt int }

func (s closedAfterSender) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	return &closedAfterResults{failAt: s.failAt}
}

type closedAfterResults struct {
	read   int
	failAt int
}

func (r *closedAfterResults) Exec() (pgconn.CommandTag, error) {
	i := r.read
	r.read++
	if i == r.failAt {
		return pgconn.CommandTag{}, closedConnError{}
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (r *closedAfterResults) Query() (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (r *closedAfterResults) QueryRow() pgx.Row { return nil }

func (r *closedAfterResults) Close() error { return nil }

// closedConnError is pgconn's "conn closed", which marks itself safe to retry
// because the statement it is reported for never reached the server. It says
// nothing about the statements sent before it, on the same connection.
type closedConnError struct{}

func (closedConnError) Error() string { return "conn closed" }

func (closedConnError) SafeToRetry() bool { return true }
