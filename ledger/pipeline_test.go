package ledger_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// tracedCounter is a counter aggregate on its own pool of conns connections,
// with every round trip counted.
func tracedCounter(t *testing.T, conns int32, opts ...ledger.StoreOption) (*ledger.Aggregate[counter, msg, int, event, effect], *tripCounter) {
	t.Helper()
	pool, trips := tracedPool(t, conns)
	return counterOn(ledger.New(pool, append([]ledger.StoreOption{ledger.RequireActor(false)}, opts...)...)), trips
}

// tracedPool is a pool of conns connections to a fresh, migrated database,
// with every round trip counted.
func tracedPool(t *testing.T, conns int32) (*pgxpool.Pool, *tripCounter) {
	t.Helper()
	cfg := ledgertest.NewStore(t).Pool().Config().Copy()
	trips := &tripCounter{}
	cfg.ConnConfig.Tracer = trips
	cfg.MaxConns = conns
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, trips
}

// Round trips are what a message costs over a network, so each path has a
// budget. Every statement is prepared before counting: preparing is a one-off
// per connection. These are the budgets with no state cache; the cache's are
// in cache_test.go.
func TestEachPathKeepsToItsRoundTrips(t *testing.T) {
	t.Parallel()
	agg, trips := tracedCounter(t, 1, ledger.StateCacheSize(0))
	agg.StateVersion, agg.SnapshotEvery = "v1", 2
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}
	key := ledger.IdempotencyKey

	paths := []struct {
		name string
		want int64
		run  func(i int) error
	}{
		{"Handle", 2, func(i int) error {
			_, err := agg.Handle(ctx, "a", add{1, 1000}, key(string(rune('a'+i))))
			return err
		}},
		// The read, then a rollback.
		{"Handle a duplicate", 2, func(int) error {
			_, err := agg.Handle(ctx, "a", add{1, 1000}, key("a"))
			return err
		}},
		{"Load", 1, func(int) error {
			_, err := agg.Load(ctx, "a")
			return err
		}},
		// BEGIN, Lock, LoadLocked, Apply, COMMIT.
		{"Transact", 5, func(int) error {
			return agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("a")}, func(tx *ledger.Tx) error {
				at, err := agg.LoadLocked(ctx, tx, "a")
				if err != nil {
					return err
				}
				_, err = agg.Apply(ctx, tx, &at, add{1, 1000})
				return err
			})
		}},
	}
	for _, p := range paths {
		// Twice to prepare, with a snapshot due on one of the next two.
		for i := range 2 {
			if err := p.run(i); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
		}
		for i := 2; i < 4; i++ {
			trips.reset()
			if err := p.run(i); err != nil {
				t.Fatalf("%s: %v", p.name, err)
			}
			if got := trips.trips.Load(); got != p.want {
				t.Errorf("%s made %d round trips, want %d", p.name, got, p.want)
			}
		}
	}
}

// A view is written in the round trip that records its entry, so it is told
// when the entry was recorded but not where in the log it landed.
func TestAViewIsToldWhenItsEntryWasRecorded(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	var cause ledger.Entry
	agg.Views = []ledger.View[counter]{{Name: "spy", Write: func(_ *ledger.Batch, _ ledger.Loaded[counter], c ledger.Entry) error {
		cause = c
		return nil
	}}}
	h, err := agg.Handle(ctx, "a", add{1, 10})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	stored := entries[0]
	if !cause.RecordedAt.Equal(stored.RecordedAt) || !h.Entry.RecordedAt.Equal(stored.RecordedAt) {
		t.Fatalf("recorded at %v; the view was told %v and Handle returned %v",
			stored.RecordedAt, cause.RecordedAt, h.Entry.RecordedAt)
	}
	if cause.Position != 0 || cause.TxID != 0 {
		t.Fatalf("the view was told a position %d and transaction %d not yet known", cause.Position, cause.TxID)
	}
	if h.Entry.Position != stored.Position || h.Entry.TxID != stored.TxID {
		t.Fatalf("Handle returned %d/%d, stored %d/%d", h.Entry.Position, h.Entry.TxID, stored.Position, stored.TxID)
	}
}

// A Handle that fails mid-transaction ends the transaction itself, so the pool
// keeps the connection instead of closing it and opening another.
func TestAFailedHandleKeepsItsConnection(t *testing.T) {
	t.Parallel()
	agg, _ := tracedCounter(t, 1)
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	opened := agg.Store.Pool().Stat().NewConnsCount()

	broken := errors.New("broken")
	agg.Views = []ledger.View[counter]{{Name: "broken", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
		b.Queue(`SELECT 1/0`)
		return nil
	}}}
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err == nil {
		t.Fatal("want the view's error")
	}
	agg.Views = []ledger.View[counter]{{Name: "refuses", Write: func(*ledger.Batch, ledger.Loaded[counter], ledger.Entry) error {
		return broken
	}}}
	if _, err := agg.Handle(ctx, "a", add{1, 10}); !errors.Is(err, broken) {
		t.Fatalf("want the view's error, got %v", err)
	}

	agg.Views = nil
	h, err := agg.Handle(ctx, "a", add{1, 10})
	if err != nil {
		t.Fatal(err)
	}
	if h.Entry.Seq != 2 {
		t.Fatalf("recorded at seq %d: a failed message left something behind", h.Entry.Seq)
	}
	if n := agg.Store.Pool().Stat().NewConnsCount(); n != opened {
		t.Fatalf("the pool opened %d connections after failures, want none", n-opened)
	}
}

// A snapshot past its stream's seq can only come from damaged data. It is
// ignored, by readers and writers alike, and state is folded from the start.
func TestASnapshotAheadOfItsStreamIsIgnored(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.StateVersion, agg.SnapshotEvery = "v1", 2
	for range 3 {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := agg.Store.Pool().Exec(ctx,
		`UPDATE cairn.state_snapshots SET seq = 99, state = '{"Total": 1000}'`); err != nil {
		t.Fatal(err)
	}

	loaded, err := agg.Load(ctx, "a")
	if err != nil || loaded.State.Total != 3 {
		t.Fatalf("loaded %+v, %v; want a total of 3", loaded, err)
	}
	h, err := agg.Handle(ctx, "a", add{1, 100})
	if err != nil || h.State.Total != 4 {
		t.Fatalf("handled %+v, %v; want a total of 4", h, err)
	}
}
