package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
)

type tally struct{ N int }

type bump struct{}

type bumped struct{}

// newTally is a tally aggregate on store, with no views.
func newTally(t *testing.T, store *Store) *Aggregate[tally, bump, int, bumped, struct{}] {
	t.Helper()
	msgs, events, effects := codec.New[bump](), codec.New[bumped](), codec.New[struct{}]()
	codec.Register[bump](msgs, "bump", 1)
	codec.Register[bumped](events, "bumped", 1)
	results := codec.New[int]()
	codec.Register[int](results, "n", 1)
	return &Aggregate[tally, bump, int, bumped, struct{}]{
		Store: store, Kind: "tally",
		Codecs: &codec.Set[bump, int, bumped, struct{}]{Messages: msgs, Results: results, Events: events, Effects: effects},
		Decider: cairn.Decider[tally, bump, int, bumped, struct{}]{
			Initial: func() tally { return tally{} },
			Decide: func(s tally, _ bump) cairn.Decision[int, bumped, struct{}] {
				return cairn.Accept[int, bumped, struct{}](s.N+1, []bumped{{}})
			},
			Evolve: func(s tally, _ bumped) tally { s.N++; return s },
		},
	}
}

// A cache can be ahead of the stored stream, when a commit it saw was later
// lost (an asynchronous commit, a failover to a lagging replica, a restore).
// The entry the cache predicts would then leave a gap the unique (stream, seq)
// index can't see. Locking at the cached seq refuses it, and the message is
// decided from the stored state instead.
func TestACacheAheadOfTheStoredStreamLeavesNoGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := New(emptyDatabase(t), RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	agg := newTally(t, store)
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
	if h.Entry.Seq != 3 || h.Result != 3 {
		t.Fatalf("recorded at seq %d with a total of %d; want 3 and 3, from the stored stream", h.Entry.Seq, h.Result)
	}
}

// A group's transaction ends only when every caller in it has given up. One
// caller's context ending must not cancel the writes the others are waiting
// for.
func TestAGroupEndsOnlyWhenEveryCallerHasGivenUp(t *testing.T) {
	t.Parallel()
	one, cancelOne := context.WithCancel(context.Background())
	defer cancelOne()
	two, cancelTwo := context.WithCancel(context.Background())
	defer cancelTwo()
	group, stop := groupContext([]*request[bump, int]{{ctx: one}, {ctx: two}})
	defer stop()

	cancelOne()
	// AfterFunc runs in a goroutine of its own, so let it run before asking
	// whether the group survived.
	time.Sleep(100 * time.Millisecond)
	if err := group.Err(); err != nil {
		t.Fatalf("one caller giving up ended the group: %v", err)
	}
	cancelTwo()
	select {
	case <-group.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the group outlived the last caller")
	}
}

// A group stops before an idempotency key it has already seen: the first
// occurrence has to be recorded before the repeat can be answered from it.
func TestAGroupStopsBeforeARepeatedIdempotencyKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		keys []string
		want int
	}{
		{"no keys", []string{"", "", ""}, 3},
		{"distinct keys", []string{"a", "b", "c"}, 3},
		{"a repeat", []string{"a", "b", "a"}, 2},
		{"the first repeats second", []string{"a", "a"}, 1},
		{"empty keys do not repeat", []string{"a", "", "a"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			reqs := make([]*request[bump, int], len(c.keys))
			for i, k := range c.keys {
				reqs[i] = &request[bump, int]{o: options{key: k}}
			}
			if got := groupLen(reqs); got != c.want {
				t.Fatalf("groupLen(%q) = %d, want %d", c.keys, got, c.want)
			}
		})
	}
}

// A caller that gives up while its message shares a transaction with others
// returns at once, and the others are still recorded: the group's transaction
// ends only when every caller in it has given up.
func TestACallerGivingUpDoesNotCancelTheOthersInItsGroup(t *testing.T) {
	// Not parallel: it holds a stream's lane open for a caller that gives up.
	ctx := context.Background()
	store := New(emptyDatabase(t), RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const waiting = 2
	agg := newTally(t, store)
	lane := laneKey{agg: agg, name: agg.Stream("a").String()}
	holding := make(chan struct{})
	inFlight := make(chan struct{})
	release := make(chan struct{})
	agg.Views = []View[tally]{{Name: "tally", Write: func(_ *Batch, at Loaded[tally], _ Entry) error {
		switch at.State.N {
		case 1:
			close(holding)
			// Hold the lane until both messages under test have queued
			// behind it, so they share one transaction.
			deadline := time.Now().Add(30 * time.Second)
			for {
				store.cache.mu.Lock()
				queued := len(store.cache.lanes[lane])
				store.cache.mu.Unlock()
				if queued == waiting {
					return nil
				}
				if time.Now().After(deadline) {
					return fmt.Errorf("only %d of %d messages reached the lane", queued, waiting)
				}
				time.Sleep(time.Millisecond)
			}
		case 3:
			// Hold the group's transaction open for one caller to give up.
			close(inFlight)
			<-release
		}
		return nil
	}}}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := agg.Handle(ctx, "a", bump{}); err != nil {
			t.Error(err)
		}
	})
	<-holding
	gone, cancel := context.WithCancel(ctx)
	var gaveUp error
	wg.Go(func() { _, gaveUp = agg.Handle(gone, "a", bump{}) })
	var waitedErr error
	wg.Go(func() { _, waitedErr = agg.Handle(ctx, "a", bump{}) })
	<-inFlight // both are recorded together, and the transaction is in flight
	cancel()   // one caller gives up
	close(release)
	wg.Wait()

	if !errors.Is(gaveUp, context.Canceled) {
		t.Fatalf("the caller that gave up got %v, want context.Canceled", gaveUp)
	}
	if waitedErr != nil {
		t.Fatalf("the caller that waited: %v", waitedErr)
	}
	entries, err := store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil || len(entries) != 3 {
		t.Fatalf("%d entries, %v; want 3", len(entries), err)
	}
	if entries[1].TxID != entries[2].TxID {
		t.Fatalf("the two callers' messages did not share a transaction: %d and %d", entries[1].TxID, entries[2].TxID)
	}
}

// A lane with more messages waiting than one transaction holds records them
// in chunks of groupMax, rather than one per transaction or all together.
func TestALaneWithMoreThanGroupMaxWaitingRecordsItInChunks(t *testing.T) {
	// Not parallel: it holds a stream's lane open while messages queue.
	ctx := context.Background()
	store := New(emptyDatabase(t), RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const queued = groupMax + 50
	agg := newTally(t, store)
	lane := laneKey{agg: agg, name: agg.Stream("a").String()}
	holding := make(chan struct{})
	agg.Views = []View[tally]{{Name: "tally", Write: func(_ *Batch, at Loaded[tally], _ Entry) error {
		if at.State.N != 1 {
			return nil
		}
		close(holding)
		// The first message holds the lane until every message under test has
		// queued behind it, so drain takes more than groupMax at once.
		deadline := time.Now().Add(30 * time.Second)
		for {
			store.cache.mu.Lock()
			waiting := len(store.cache.lanes[lane])
			store.cache.mu.Unlock()
			if waiting == queued {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("only %d of %d messages reached the lane", waiting, queued)
			}
			time.Sleep(time.Millisecond)
		}
	}}}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := agg.Handle(ctx, "a", bump{}); err != nil {
			t.Error(err)
		}
	})
	<-holding
	errs := make([]error, queued)
	for i := range queued {
		wg.Go(func() { _, errs[i] = agg.Handle(ctx, "a", bump{}) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}

	entries, err := store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil || len(entries) != queued+1 {
		t.Fatalf("%d entries, %v; want %d", len(entries), err, queued+1)
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
	}

	rows, err := store.Pool().Query(ctx, `SELECT count(*) FROM cairn.entries GROUP BY txid ORDER BY count(*) DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sizes []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[0] != groupMax || sizes[1] != queued-groupMax {
		t.Fatalf("transactions recorded %v entries, want %d, %d and 1", sizes, groupMax, queued-groupMax)
	}
}

// Trust is a moving average, not the last answer: one stale decision does not
// stop the cache deciding from a stream, a run of them does, and right
// decisions bring it back.
func TestTrustInACachedStreamDecaysAndRecovers(t *testing.T) {
	t.Parallel()
	cache := newStateCache(4)
	const key = "tally/a"
	cache.put(key, tally{N: 1})
	if _, trust := cache.begin(key); !trust {
		t.Fatal("a stream new to the cache was not trusted")
	}
	cache.observe(key, false)
	if _, trust := cache.begin(key); !trust {
		t.Fatal("one stale decision stopped the cache deciding from the stream")
	}
	distrusted := false
	for range 100 {
		cache.observe(key, false)
		if _, trust := cache.begin(key); !trust {
			distrusted = true
			break
		}
	}
	if !distrusted {
		t.Fatal("the cache never stopped trusting a stream that kept going stale")
	}
	trusted := false
	for range 100 {
		cache.observe(key, true)
		if _, trust := cache.begin(key); trust {
			trusted = true
			break
		}
	}
	if !trusted {
		t.Fatal("the cache never trusted the stream again")
	}
}
