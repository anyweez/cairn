package ledger

import (
	"context"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
)

type tally struct{ N int }

type bump struct{}

type bumped struct{}

// A cache can be ahead of the stored stream, when a commit it saw was later
// lost (an asynchronous commit, a failover to a lagging replica, a restore).
// The entry the cache predicts would then leave a gap the unique (stream, seq)
// index can't see. Locking at the cached seq refuses it, and the message is
// decided from the stored state instead.
func TestACacheAheadOfTheStoredStreamLeavesNoGap(t *testing.T) {
	ctx := context.Background()
	store := New(emptyDatabase(t), RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	msgs, events, effects := codec.New[bump](), codec.New[bumped](), codec.New[struct{}]()
	codec.Register[bump](msgs, "bump", 1)
	codec.Register[bumped](events, "bumped", 1)
	agg := &Aggregate[tally, bump, int, bumped, struct{}]{
		Store: store, Kind: "tally", Messages: msgs, Events: events, Effects: effects,
		Decider: cairn.Decider[tally, bump, int, bumped, struct{}]{
			Initial: func() tally { return tally{} },
			Decide: func(s tally, _ bump) cairn.Decision[int, bumped, struct{}] {
				return cairn.Accept[int, bumped, struct{}](s.N+1, []bumped{{}})
			},
			Evolve: func(s tally, _ bumped) tally { s.N++; return s },
		},
	}
	for range 2 {
		if _, err := agg.Handle(ctx, "a", bump{}); err != nil {
			t.Fatal(err)
		}
	}

	stream := agg.Stream("a")
	store.cache.put(agg.cacheKey(stream), Loaded[tally]{Stream: stream, State: tally{N: 5}, Seq: 5})
	h, err := agg.Handle(ctx, "a", bump{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Entry.Seq != 3 || h.State.N != 3 {
		t.Fatalf("recorded at seq %d with a total of %d; want 3 and 3, from the stored stream", h.Entry.Seq, h.State.N)
	}
}
