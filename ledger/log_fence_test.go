package ledger

import (
	"context"
	"testing"
)

// widenedXid renumbers the 32-bit xids the fence reads (a live one's in
// pg_stat_activity.backend_xid, a prepared one's in pg_prepared_xacts.transaction)
// onto the 64-bit scale cairn.entries.txid is stored and ordered on, because
// those columns carry only the low 32 bits of the transaction ID.
//
// CI's clusters are fresh, so the epoch is always 0 there and no integration
// test can tell wrong widening from right. This one crafts the reference and
// the xids instead, on both sides of the wrap boundary.
func TestWidenedXidsHitTheWrapBoundaryOnBothSides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	// crafted names the 32-bit xid (thumb), the reference it widens under
	// (ref_low = the low 32 bits of the next xid, ref_epoch its epoch), and
	// the xid8 the widening has to produce (want).
	cases := []struct {
		thumb    int64
		refLow   int64
		refEpoch int64
		want     string
		what     string
	}{
		{50, 100, 1, "4294967346", "same epoch as the next xid"},
		{4294967290, 100, 1, "4294967290", "above the next xid's low half: previous epoch"},
		{100, 100, 1, "4294967396", "at the boundary: same epoch (no transaction can carry the next-to-assign xid)"},
		{4294967295, 0, 1, "4294967295", "just below the reference's low half: previous epoch"},
		{4294967294, 4294967295, 0, "4294967294", "epoch 0 left alone: the previous epoch does not exist"},
	}
	const check = `(SELECT ` + widenedXid + `
        FROM (SELECT c.thumb::numeric AS thumb) AS x
        CROSS JOIN (SELECT c.ref_low::numeric AS low, c.ref_epoch::numeric AS epoch) AS ref)
      = c.want::text::xid8`
	for _, c := range cases {
		var ok bool
		if err := pool.QueryRow(ctx,
			`WITH crafted(thumb, ref_low, ref_epoch, want) AS
             (VALUES ($1::numeric, $2::numeric, $3::numeric, $4::bigint))
             SELECT `+check+` FROM crafted AS c`,
			c.thumb, c.refLow, c.refEpoch, c.want).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("widening %d under (low %d, epoch %d): want %s — %s",
				c.thumb, c.refLow, c.refEpoch, c.want, c.what)
		}
	}
}

// With nothing else in flight in the database, the in-flight set is empty —
// pg_stat_activity is read before this statement is assigned its own xid, so
// its own backend_xid is not in it — and the fence has to bound itself with
// the measuring transaction's own xid rather than fall back to the sentinel.
// At the sentinel there is no cutoff at all: a transaction assigned an xid
// after the fence can have a later-assigned, already-committed transaction's
// entries returned ahead of it, so the cursor passes it and its own entry is
// skipped when it commits.
func TestAFenceOnAnIdleDatabaseIsTheMeasuringTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The fence goes first, while this transaction has no xid yet. Read after
	// a statement that already assigned one, it would be in the in-flight set
	// and the empty case would never be reached.
	var fence string
	if err := tx.QueryRow(ctx, logFence).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	if fence == "18446744073709551615" {
		t.Fatalf("the fence is the sentinel with no other transaction open: no cutoff, so entries committed ahead of a transaction assigned after the fence are returned past its cursor and its own entry is skipped")
	}

	var xid string
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
		t.Fatal(err)
	}
	if fence != xid {
		t.Fatalf("the fence is %s on an otherwise idle database, want the measuring transaction's own %s", fence, xid)
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
