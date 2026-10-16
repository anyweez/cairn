package ledger_test

import (
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// A projection reads one database's entries, so an open transaction in a
// different database on the same Postgres server must not stall it.
func TestAProjectionIsNotStalledByAnotherDatabasesTransaction(t *testing.T) {
	agg := newCounter(t)
	other := newCounter(t) // a second, unrelated database on the same server

	// Hold a transaction, and an xid, open in the other database for the
	// length of the catch-up.
	tx, err := other.Store.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var xid string
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
		t.Fatal(err)
	}

	if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}
	projection := seenProjection(t, agg)
	n, err := projection.CatchUp(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the projection applied %d entries while database %s held transaction %s open; want 1",
			n, other.Store.Pool().Config().ConnConfig.Database, xid)
	}
}

// A prepared transaction is invisible until it commits, but its transaction ID
// still orders it before later entries. An older prepared transaction must
// bound the read cutoff even while a newer ordinary transaction is open, or
// its commit lands behind a cursor that already passed it.
func TestAnOlderPreparedTransactionIsNotSkipped(t *testing.T) {
	agg := newCounter(t)

	var maxPrepared int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT current_setting('max_prepared_transactions')::int`).Scan(&maxPrepared); err != nil {
		t.Fatal(err)
	}
	if maxPrepared == 0 {
		t.Skip("max_prepared_transactions is 0, so PREPARE TRANSACTION is disabled")
	}

	// Prepare an entry, giving it the oldest transaction ID in play.
	gid := "cairn_test_prepared"
	conn, err := agg.Store.Pool().Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared := agg.Stream("prepared")
	if _, err := tx.Exec(ctx, `INSERT INTO cairn.streams (stream, kind, seq) VALUES ($1, $2, 1)`, prepared.String(), "counter"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO cairn.entries (stream, kind, seq, message) VALUES ($1, $2, 1, '{}')`, prepared.String(), "counter"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `PREPARE TRANSACTION '`+gid+`'`); err != nil {
		t.Fatal(err)
	}

	// A committed entry with a newer transaction ID, which a reader could
	// advance its cursor past while the prepared transaction is still open.
	if _, err := agg.Handle(ctx, "after", add{1, 10}); err != nil {
		t.Fatal(err)
	}

	// A newer ordinary transaction, open for the length of the read: with a
	// cutoff that took the first non-null minimum, this would mask the older
	// prepared one.
	ordinary, err := agg.Store.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ordinary.Rollback(ctx) }()
	if _, err := ordinary.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		t.Fatal(err)
	}

	entries, cursor, err := agg.Store.ReadAll(ctx, ledger.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read %d entries while an older prepared transaction was open: %+v", len(entries), entries)
	}

	if _, err := agg.Store.Pool().Exec(ctx, `COMMIT PREPARED '`+gid+`'`); err != nil {
		t.Fatal(err)
	}

	// The prepared transaction committed after the read, but its transaction
	// ID is older, so a cursor past it would skip it forever.
	var seen []ledger.Entry
	for deadline := time.Now().Add(10 * time.Second); len(seen) < 2 && time.Now().Before(deadline); {
		more, next, err := agg.Store.ReadAll(ctx, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(more) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		seen, cursor = append(seen, more...), next
	}
	if len(seen) != 2 || seen[0].Stream != prepared {
		t.Fatalf("after the prepared transaction committed, read %+v; want its entry first", seen)
	}
}
