package ledger_test

import (
	"testing"
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
