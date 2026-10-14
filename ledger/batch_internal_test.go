package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
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

// Postgres runs every statement in a batch before the caller reads any result,
// so a handler can fail in Go — a scan or a parse — after the statements,
// including the COMMIT, have already run. The caller gets an error either way,
// but it must say the message was recorded rather than that the transaction
// failed, or a retry records it a second time.
func TestABatchThatCommittedDespiteAFailedResultSaysSo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE committed (n int)`); err != nil {
		t.Fatal(err)
	}

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.own(scanOneInt, `SELECT 'not an integer'`)
	b.own(commitStep, `COMMIT`)
	b.commits = true

	err = b.send(ctx, conn)
	if !errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, want ErrRecorded", err)
	}
	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM committed`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the batch's insert is %d rows, want 1: the error does not describe what happened", rows)
	}
}

// A cached Handle sends one batch with no BEGIN, which the connection runs as
// an implicit transaction and commits as it ends. A client-side failure there
// hides a commit in the same way.
func TestAnImplicitTransactionThatCommittedSaysSo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `CREATE TEMP TABLE committed (n int)`); err != nil {
		t.Fatal(err)
	}

	var b Batch
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.own(scanOneInt, `SELECT 'not an integer'`)
	b.commits = true

	err = b.send(ctx, conn)
	if !errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, want ErrRecorded", err)
	}
	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM committed`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("the batch's insert is %d rows, want 1: the implicit transaction did not commit", rows)
	}
}

// A batch sent inside a transaction another statement ends has not committed
// when a handler fails, so the error must not claim it did.
func TestABatchThatDidNotCommitDoesNotSayItDid(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
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

// A statement the database refuses aborts the transaction, so the commit did
// not happen and the error must not say it did.
func TestABatchRefusedByTheDatabaseDoesNotSayItCommitted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(scanOneInt, `SELECT 1/0`)
	b.own(commitStep, `COMMIT`)
	b.commits = true

	err = b.send(ctx, conn)
	if err == nil {
		t.Fatal("send returned nil, want the database's error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, but the transaction was aborted", err)
	}
}
