package ledger

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// widenedXid renumbers the 32-bit xids the fence reads (a live one's in
// pg_stat_activity.backend_xid, a prepared one's in pg_prepared_xacts.transaction)
// onto the 64-bit scale cairn.entries.txid is stored and ordered on, against
// the reading statement's snapshot frontier, because those columns carry only
// the low 32 bits of the transaction ID.
//
// CI's clusters are fresh, so the epoch is always 0 there and no integration
// test can tell wrong widening from right. This one crafts the reference and
// the xids instead, on both sides of the wrap boundary.
func TestWidenedXidsHitTheWrapBoundaryOnBothSides(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	// crafted names the 32-bit xid (thumb), the xid8 frontier it widens
	// against (xmax), and the xid8 the widening has to produce (want).
	cases := []struct {
		thumb int64
		xmax  int64
		want  string
		what  string
	}{
		{50, 4294967396, "4294967346", "below the frontier: same epoch"},
		{4294967290, 4294967396, "4294967290", "far above its low half: previous epoch"},
		{100, 4294967396, "4294967396", "at the boundary: the frontier itself"},
		{4294967295, 4294967296, "4294967295", "one below across the wrap: previous epoch"},
		{0, 8589934591, "8589934592", "one above across the wrap: next epoch"},
		// The frontier is the latest completed transaction plus one, so the
		// newest assigned transactions — in flight under any load — sit just
		// above it. Those must widen to themselves, not to the previous
		// epoch: read low they would drop the cutoff 2^32, and a transaction
		// committing in that window could be read past.
		{300, 250, "300", "the newest in-flight xid, just above the frontier"},
		{105, 4294967396, "4294967401", "a few above its low half past epoch 0: still its epoch"},
		{3, 4294967296, "4294967299", "a few above its low half at the wrap boundary"},
	}
	const check = `(SELECT ` + widenedXid + `
        FROM (SELECT c.thumb::numeric AS thumb) AS x
        CROSS JOIN (SELECT c.xmax::numeric AS xmax) AS ref)
      = c.want::text::xid8`
	for _, c := range cases {
		var ok bool
		if err := pool.QueryRow(ctx,
			`WITH crafted(thumb, xmax, want) AS
             (VALUES ($1::numeric, $2::numeric, $3::bigint))
             SELECT `+check+` FROM crafted AS c`,
			c.thumb, c.xmax, c.want).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("widening %d against %d: want %s — %s",
				c.thumb, c.xmax, c.want, c.what)
		}
	}
}

// A standalone ReadAll is a pure read, and it has to stay one. Assigning the
// reading transaction a transaction ID would use one up on every poll — which
// hastens the epoch rollover the fence's widening exists to cross — write a
// commit WAL record for a read, and fail outright on a hot standby, where no
// transaction may be assigned an ID. The fence takes its reference from the
// statement's own snapshot instead. A caller's transaction is where that is
// observable: run the read on one and it must come back with no ID assigned.
func TestReadAllTakesNoTransactionID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	if err := New(pool, RequireActor(false)).Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := readAll(ctx, tx, Cursor{}, 10, nil); err != nil {
		t.Fatal(err)
	}
	var assigned *string
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id_if_assigned()::text`).Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	if assigned != nil {
		t.Fatalf("readAll assigned transaction %s: every poll of the log would use up an xid and write a commit record, and a hot standby could not run it", *assigned)
	}
}

// The fence is capped by the snapshot frontier, which sits at or below the
// newest assigned transactions: those took their xids after the last commit
// and are still in flight. Widening one of them with "above the reference's
// low half is the previous epoch" put it — and the cutoff — a whole 2^32 low,
// and a transaction committing in that window could be read past. Read by
// signed distance the thumb stays just above the frontier, and the LEAST holds
// the fence there.
//
// On a fresh cluster (epoch 0) that mis-widen wrapped the negative result to a
// huge xid8 instead of going low, so this test only catches the regression on
// a cluster past epoch 0; the crafted cases above are the guard CI can see.
func TestTheFenceHoldsNewerTransactionsWithoutDroppingAnEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	// Transactions that take their xids after the frontier and stay open, so
	// the fence's view holds thumbs above the number it widens against.
	var held []uint64
	for range 2 {
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
		var xid string
		if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id()::text`).Scan(&xid); err != nil {
			t.Fatal(err)
		}
		v, err := strconv.ParseUint(xid, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, v)
	}
	oldest := min(held[0], held[1])

	fence, err := readXid8(ctx, pool, logFence)
	if err != nil {
		t.Fatal(err)
	}
	// A later statement's frontier is at least the fence statement's: it can
	// only have risen, when another database committed in between.
	frontier, err := readXid8(ctx, pool, `SELECT pg_snapshot_xmax(pg_current_snapshot())::text`)
	if err != nil {
		t.Fatal(err)
	}
	if fence > frontier {
		t.Fatalf("the fence is %d, above the snapshot frontier %d that caps it", fence, frontier)
	}
	if fence > oldest {
		t.Fatalf("the fence is %d while transaction %d is in flight: it passed a transaction that had not finished, so an entry committing late can be read past", fence, oldest)
	}
	if oldest-fence >= 1<<31 {
		t.Fatalf("the fence is %d while the oldest in-flight transaction is %d: a whole epoch low, so this database's entries are held back until it finishes", fence, oldest)
	}
}

// readXid8 runs a single-column xid8 query and returns its value as a number.
func readXid8(ctx context.Context, pool *pgxpool.Pool, sql string) (uint64, error) {
	var text string
	if err := pool.QueryRow(ctx, sql).Scan(&text); err != nil {
		return 0, err
	}
	return strconv.ParseUint(text, 10, 64)
}

// At epoch 0 the widening must agree digit for digit with the transaction ID
// Postgres itself assigns, so a live transaction in this database widens to
// itself and cannot be mistaken for one a wrap away. This wires the widening
// into the fence — the aliases, the cross join, the LEAST — not just the
// arithmetic above. The fence it produces sits at the snapshot frontier, at or
// below the live transaction and in the same epoch, so its entries wait for it
// to finish instead of being skipped or held back indefinitely.
func TestALiveTransactionWidensToItselfAndTheFenceHoldsIt(t *testing.T) {
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

	var fenceText string
	if err := pool.QueryRow(ctx, logFence).Scan(&fenceText); err != nil {
		t.Fatal(err)
	}
	fence, err := strconv.ParseUint(fenceText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	live, err := strconv.ParseUint(xid, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if fence > live {
		t.Fatalf("the fence is %d while this database holds transaction %d open: it would return entries from beyond a transaction still in flight", fence, live)
	}
	if live-fence >= 1<<31 {
		t.Fatalf("the fence is %d while the newest live transaction here is %d: a whole epoch low, so every entry waits for it to finish", fence, live)
	}
}
