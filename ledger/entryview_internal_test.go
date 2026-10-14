package ledger

import (
	"context"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
)

// A rebuild replays a stream a window at a time, each in its own transaction,
// carrying the folded state from one window to the next.
func TestRebuildEntriesCarriesStateAcrossWindows(t *testing.T) {
	// Not parallel: it changes rebuildWindow, which parallel tests wait out.
	defer func(n int) { rebuildWindow = n }(rebuildWindow)
	rebuildWindow = 2

	ctx := context.Background()
	store := New(emptyDatabase(t), RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, events, effects, results := codec.New[bump](), codec.New[bumped](), codec.New[struct{}](), codec.New[int]()
	codec.Register[bump](msgs, "bump", 1)
	codec.Register[bumped](events, "bumped", 1)
	codec.Register[int](results, "n", 1)
	agg := &Aggregate[tally, bump, int, bumped, struct{}]{
		Store: store, Kind: "tally", Messages: msgs, Events: events, Effects: effects, Results: results,
		Decider: cairn.Decider[tally, bump, int, bumped, struct{}]{
			Initial: func() tally { return tally{} },
			Decide: func(s tally, _ bump) cairn.Decision[int, bumped, struct{}] {
				return cairn.Accept[int, bumped, struct{}](s.N+1, []bumped{{}})
			},
			Evolve: func(s tally, _ bumped) tally { s.N++; return s },
		},
	}
	for range 5 {
		if _, err := agg.Handle(ctx, "a", bump{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.pool.Exec(ctx, `CREATE TABLE tally_history (seq bigint PRIMARY KEY, before int NOT NULL, after int NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	v := EntryView[tally, bumped]{Name: "tally_history",
		Write: func(b *Batch, before, after Loaded[tally], e Entry, _ []bumped) error {
			b.Queue(`INSERT INTO tally_history VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, e.Seq, before.State.N, after.State.N)
			return nil
		}}
	if n, err := agg.RebuildEntries(ctx, v); err != nil || n != 1 {
		t.Fatalf("rebuilt %d streams, %v", n, err)
	}

	var rows, wrong int
	if err := store.pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE before <> seq - 1 OR after <> seq) FROM tally_history`).Scan(&rows, &wrong); err != nil {
		t.Fatal(err)
	}
	if rows != 5 || wrong != 0 {
		t.Fatalf("%d rows, %d with the wrong state; want 5 continuing across three windows", rows, wrong)
	}
}
