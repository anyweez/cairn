package ledger

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// widenedXid renumbers the 32-bit xids the fence reads (a live one's in
// pg_stat_activity.backend_xid, a prepared one's in pg_prepared_xacts.transaction)
// onto the 64-bit scale cairn.entries.txid is stored and ordered on, against
// the measuring transaction's own xid, because those columns carry only the
// low 32 bits of the transaction ID.
//
// CI's clusters are fresh, so the epoch is always 0 there and no integration
// test can tell wrong widening from right. This one crafts the reference and
// the xids instead, on both sides of the wrap boundary.
func TestWidenedXidsHitTheWrapBoundaryOnBothSides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	// crafted names the 32-bit xid (thumb), the measuring xid8 it widens
	// against (own), and the xid8 the widening has to produce (want).
	cases := []struct {
		thumb int64
		own   int64
		want  string
		what  string
	}{
		{50, 4294967396, "4294967346", "below the measuring xid: same epoch"},
		{4294967290, 4294967396, "4294967290", "far above its low half: previous epoch"},
		{100, 4294967396, "4294967396", "at the boundary: the measuring xid itself"},
		{4294967295, 4294967296, "4294967295", "one below across the wrap: previous epoch"},
		{0, 8589934591, "8589934592", "one above across the wrap: next epoch"},
		// The fence reads the view after own was assigned, so it can hold xids a
		// few newer than own. Those must stay above own, not wrap back 2^32.
		// Both of these widened a whole epoch too low before the signed
		// distance, and the first of them did so at epoch 0 too.
		{105, 4294967396, "4294967401", "a few above its low half: still its epoch, not the previous one"},
		{3, 4294967296, "4294967299", "a few above its low half at the wrap boundary"},
		{105, 100, "105", "a few above at epoch 0: still this epoch"},
	}
	const check = `(SELECT ` + widenedXid + `
        FROM (SELECT c.thumb::numeric AS thumb) AS x
        CROSS JOIN (SELECT c.own::numeric AS own) AS ref)
      = c.want::text::xid8`
	for _, c := range cases {
		var ok bool
		if err := pool.QueryRow(ctx,
			`WITH crafted(thumb, own, want) AS
             (VALUES ($1::numeric, $2::numeric, $3::bigint))
             SELECT `+check+` FROM crafted AS c`,
			c.thumb, c.own, c.want).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("widening %d against %d: want %s — %s",
				c.thumb, c.own, c.want, c.what)
		}
	}
}

// readAll assigns the measuring transaction's xid in a statement of its own,
// ahead of the fence, so that every transaction the fence's view holds was
// assigned its xid before or after own, never in between. The statements have
// to be one transaction, and they have to run in that order. This runs the
// pair in readAll's shape — one batch on the pool, ended by a single Sync —
// and asserts the fence is the xid the first statement assigned: in separate
// transactions the fence would measure the later one.
func TestAFenceIsTheXidItsBatchAssigned(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	var b pgx.Batch
	b.Queue(`SELECT pg_current_xact_id()`)
	b.Queue(logFence)
	results := pool.SendBatch(ctx, &b)
	var assigned, fence string
	if err := results.QueryRow().Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if err := results.QueryRow().Scan(&fence); err != nil {
		_ = results.Close()
		t.Fatal(err)
	}
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	if fence != assigned {
		t.Fatalf("the fence is %s after a batch that assigned %s: the statements did not share a transaction, so the fence was measured after an xid later than the one the view was read after", fence, assigned)
	}
}

// The view is read after the measuring transaction's xid was assigned, so it
// can hold transactions assigned a few xids after own and still in flight.
// Widening one of those against a reference derived from own used to put it a
// whole 2^32 below — the previous epoch — and the fence went with it, so a
// transaction committing in that window could be read past.
//
// On a fresh cluster (epoch 0) the old code's off-by-one wrapped the negative
// result to a huge xid8 instead of going low, so this test only catches the
// regression on a cluster past epoch 0; the crafted cases above are the guard
// CI can see.
func TestAFenceIsNotDroppedByTransactionsAssignedAfterTheMeasuringOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	measuring, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer measuring.Release()
	tx, err := measuring.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var own string
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&own); err != nil {
		t.Fatal(err)
	}

	// Two transactions that take their xids after own and stay open, so the
	// fence's view holds thumbs newer than the xid it widens against.
	for range 2 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		newer, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = newer.Rollback(ctx) }()
		var after bool
		if err := newer.QueryRow(ctx,
			`SELECT pg_current_xact_id()::text::numeric > $1::text::numeric`, own).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if !after {
			t.Fatalf("a held transaction took an xid at or below the measuring %s", own)
		}
	}

	var fence string
	if err := tx.QueryRow(ctx, logFence).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if fence != own {
		t.Fatalf("the fence is %s while %s and newer transactions are in flight: it dropped below the measuring xid, so a transaction committing in the window can be read past", fence, own)
	}
}

// At epoch 0 the widening must agree digit for digit with the transaction ID
// Postgres itself assigns, so a live transaction in this database widens to
// itself and the fence sits on it. This wires the widening into the fence —
// the aliases, the cross join, the LEAST — not just the arithmetic above.
func TestAFenceOnAFreshClusterSitsOnLiveTransactionIds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	// The same subqueries logFence runs over pg_stat_activity, alone.
	const widenedBackend = `(SELECT min(` + widenedXid + `)
        FROM (SELECT backend_xid::text::numeric AS thumb FROM pg_stat_activity
              WHERE datid = (SELECT oid FROM pg_database WHERE datname = current_database())) AS x(thumb)
        CROSS JOIN ` + fenceRef + ` AS ref)`

	held, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	tx, err := held.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var xid string
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
		t.Fatal(err)
	}

	var widened string
	if err := pool.QueryRow(ctx, `SELECT `+widenedBackend+`::text`).Scan(&widened); err != nil {
		t.Fatal(err)
	}
	if widened != xid {
		t.Fatalf("live transaction %s widened to %s, want itself; a fence reading it lower stalls every read", xid, widened)
	}

	var fence string
	if err := pool.QueryRow(ctx, logFence).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if fence != xid {
		t.Fatalf("the fence is %s while this database holds transaction %s open; want %s", fence, xid, xid)
	}
}
