package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// handleAll sends msgs to one stream at once, and returns each one's outcome
// in the order given.
func handleAll(agg *ledger.Aggregate[counter, msg, int, event, effect], id string, msgs []msg, opts ...ledger.Option) ([]ledger.Handled[int], []error) {
	hs := make([]ledger.Handled[int], len(msgs))
	errs := make([]error, len(msgs))
	var wg sync.WaitGroup
	for i, m := range msgs {
		wg.Go(func() { hs[i], errs[i] = agg.Handle(ctx, id, m, opts...) })
	}
	wg.Wait()
	return hs, errs
}

// checkStream fails unless the stream's entries run 1..n with no gaps, and
// its stored state totals want.
func checkStream(t *testing.T, agg *ledger.Aggregate[counter, msg, int, event, effect], id string, n int64, want int) {
	t.Helper()
	entries, err := agg.Store.ReadStream(ctx, agg.Stream(id), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) {
			t.Fatalf("entry %d has seq %d", i, e.Seq)
		}
	}
	if int64(len(entries)) != n {
		t.Fatalf("%d entries, want %d", len(entries), n)
	}
	loaded, err := agg.Load(ctx, id)
	if err != nil || loaded.State.Total != want {
		t.Fatalf("stored total %d, %v; want %d", loaded.State.Total, err, want)
	}
}

func TestACachedStreamCostsOneRoundTrip(t *testing.T) {
	t.Parallel()
	agg, trips := tracedCounter(t, 1)
	agg.StateVersion, agg.SnapshotEvery = "v1", 3
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}

	handle := func() int64 {
		t.Helper()
		trips.reset()
		if _, err := agg.Handle(ctx, "a", add{1, 100}, ledger.IdempotencyKey(fmt.Sprint(time.Now().UnixNano()))); err != nil {
			t.Fatal(err)
		}
		return trips.trips.Load()
	}
	if n := handle(); n != 2 {
		t.Fatalf("an uncached stream took %d round trips, want 2", n)
	}
	handle() // prepares the cached path's statements
	for range 4 {
		if n := handle(); n != 1 {
			t.Fatalf("a cached stream took %d round trips, want 1", n)
		}
	}
	checkStream(t, agg, "a", 6, 6)
}

// Another process writing the stream leaves the cache behind it. The stale
// decision is never recorded: the message is decided again from the stored
// state. (A cache ahead of the stream is tested inside the package.)
func TestACacheNeverRecordsAgainstStaleState(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	// Another process: its own store, and its own cache.
	other := counterOn(ledger.New(agg.Store.Pool(), ledger.RequireActor(false)))

	if _, err := agg.Handle(ctx, "a", add{3, 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Handle(ctx, "a", add{2, 5}); err != nil {
		t.Fatal(err)
	}
	// Cached, a total of 3 would accept this; stored, it is 5, which refuses.
	h, err := agg.Handle(ctx, "a", add{1, 5})
	if !errors.Is(err, &cairn.Refusal{Code: "over_cap"}) || h.Entry.Seq != 3 {
		t.Fatalf("decided against stale state: %+v, %v", h, err)
	}
	if h, err := agg.Handle(ctx, "a", add{0, 5}); err != nil || h.Entry.Seq != 4 || h.Result != 5 {
		t.Fatalf("after catching up: %+v, %v", h, err)
	}
	checkStream(t, agg, "a", 4, 5)
}

func TestMessagesArrivingTogetherShareATransaction(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}
	const n = 60
	hs, errs := handleAll(agg, "a", slices.Repeat([]msg{add{1, 1000}}, n))

	var totals []int
	for i := range hs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		totals = append(totals, hs[i].Result)
	}
	slices.Sort(totals)
	for i, total := range totals {
		if total != i+1 {
			t.Fatalf("each caller should see the total its own message made: %v", totals)
		}
	}
	checkStream(t, agg, "a", n, n)

	var txs int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(DISTINCT txid) FROM cairn.entries`).Scan(&txs); err != nil {
		t.Fatal(err)
	}
	if txs >= n {
		t.Fatalf("%d messages took %d transactions; none were recorded together", n, txs)
	}
	// The view is written once per transaction, from the state after it.
	if got := readTotals(t, agg.Store.Pool()); len(got) != 1 || got[0].Total != n || got[0].Seq != n {
		t.Fatalf("view %v, want the final total", got)
	}
}

// Messages recorded together are decided in order, each against the state
// the ones before it left.
func TestMessagesRecordedTogetherSeeTheOnesBeforeThem(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	hs, errs := handleAll(agg, "a", slices.Repeat([]msg{add{1, 10}}, 30))
	accepted := 0
	for i := range hs {
		switch {
		case errs[i] == nil:
			accepted++
		case !errors.Is(errs[i], &cairn.Refusal{Code: "over_cap"}):
			t.Fatal(errs[i])
		}
	}
	if accepted != 10 {
		t.Fatalf("%d accepted, want exactly the cap of 10", accepted)
	}
	checkStream(t, agg, "a", 30, 10)
}

func TestARetryThroughTheCacheGetsItsFirstAnswer(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	first, err := agg.Handle(ctx, "a", add{4, 10}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := agg.Handle(ctx, "a", add{4, 10}, ledger.IdempotencyKey("k"))
	if err != nil || !again.Duplicate || again.Result != first.Result || again.Entry.Seq != 1 {
		t.Fatalf("retry: %+v, %v", again, err)
	}

	// The same key several times at once: one is recorded, the rest answered
	// from it.
	hs, errs := handleAll(agg, "a", slices.Repeat([]msg{add{1, 10}}, 8), ledger.IdempotencyKey("k2"))
	recorded := 0
	for i := range hs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !hs[i].Duplicate {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("one key recorded %d times", recorded)
	}
	checkStream(t, agg, "a", 2, 5)

	// A retry that lost its metadata still gets its first answer, though its
	// stream is cached and the store requires an actor.
	strict := counterOn(ledger.New(agg.Store.Pool()))
	as := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana"})
	if _, err := strict.Handle(as, "b", add{1, 10}, ledger.IdempotencyKey("k3")); err != nil {
		t.Fatal(err)
	}
	if h, err := strict.Handle(ctx, "b", add{1, 10}, ledger.IdempotencyKey("k3")); err != nil || !h.Duplicate {
		t.Fatalf("a retry without an actor: %+v, %v", h, err)
	}
}

// When one message's view fails in the database, it spoils the transaction
// it shares with others. The others are then recorded one by one, and only
// the message whose view fails is refused.
func TestOnlyTheMessageWhoseViewFailsIsLost(t *testing.T) {
	// Not parallel: sleeps order the writes, and load from other tests could
	// reorder them.
	agg := newCounter(t)
	view := totalsView(t, agg)
	var writes atomic.Int64
	agg.Views = []ledger.View[counter]{{Name: "counter_totals", Write: func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
		writes.Add(1)
		switch at.State.Total {
		case 1:
			// Holds the stream's lane while the next messages queue behind it.
			b.Queue(`SELECT pg_sleep(0.3)`)
		case 6:
			b.Queue(`SELECT 1/0`)
		}
		return view.Write(b, at, cause)
	}}}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Error(err)
		}
	})
	time.Sleep(100 * time.Millisecond)
	hs, errs := handleAll(agg, "a", slices.Repeat([]msg{add{1, 100}}, 5))
	wg.Wait()

	var failed int
	for i := range hs {
		if errs[i] != nil {
			failed++
			if !strings.Contains(errs[i].Error(), "division by zero") {
				t.Fatalf("want the view's error, got %v", errs[i])
			}
		}
	}
	if failed != 1 {
		t.Fatalf("%d messages failed, want only the one whose view fails", failed)
	}
	checkStream(t, agg, "a", 5, 5)
	// Recorded one at a time from the start, the six messages would write the
	// view six times. Grouped, the five queued ones write it twice more first:
	// from the cache, then from the stored state, before the fallback.
	if n := writes.Load(); n <= 6 {
		t.Fatalf("the view was written %d times: the queued messages were never recorded together", n)
	}
}

// A transaction whose last message is a refusal writes its views as of the
// last entry that changed the state, as Rebuild would.
func TestAViewWrittenForSeveralMessagesHasTheSeqOfItsCause(t *testing.T) {
	// Not parallel: sleeps order the writes, and load from other tests could
	// reorder them.
	agg := newCounter(t)
	view := totalsView(t, agg)
	agg.Views = []ledger.View[counter]{{Name: "counter_totals", Write: func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
		if at.State.Total == 1 {
			b.Queue(`SELECT pg_sleep(0.3)`) // holds the lane while two more queue
		}
		return view.Write(b, at, cause)
	}}}

	var wg sync.WaitGroup
	for i, m := range []msg{add{1, 3}, add{1, 3}, add{100, 3}} {
		wg.Go(func() { _, _ = agg.Handle(ctx, "a", m) })
		if i == 0 {
			time.Sleep(100 * time.Millisecond)
		} else {
			time.Sleep(20 * time.Millisecond) // the refusal queues last
		}
	}
	wg.Wait()

	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil || len(entries) != 3 || entries[2].Refusal == nil {
		t.Fatalf("entries %+v, %v; want two accepted, then a refusal", entries, err)
	}
	if got := readTotals(t, agg.Store.Pool()); len(got) != 1 || got[0].Total != 2 || got[0].Seq != 2 {
		t.Fatalf("view %v, want a total of 2 at seq 2, the entry that made it", got)
	}
}

// The composable API records every entry of one transaction at one time.
func TestOneTransactionsEntriesShareTheirRecordingTime(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	err := agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("a"), agg.Stream("b")}, func(tx *ledger.Tx) error {
		for _, id := range []string{"a", "b"} {
			at, err := agg.LoadLocked(ctx, tx, id)
			if err != nil {
				return err
			}
			time.Sleep(5 * time.Millisecond)
			if _, err := agg.Apply(ctx, tx, &at, add{1, 10}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	b, _ := agg.Store.ReadStream(ctx, agg.Stream("b"), 0)
	if len(a) != 1 || len(b) != 1 || !a[0].RecordedAt.Equal(b[0].RecordedAt) || a[0].TxID != b[0].TxID {
		t.Fatalf("one transaction's entries: %+v and %+v", a, b)
	}
}

// A kind with a slash in it could name another kind's stream ("a/b" + "c" is
// "a" + "b/c"). Validate refuses it before anything is cached or read.
func TestAnInvalidKindIsRefusedOnACachedStream(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "b/c", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	alias := *agg
	alias.Kind = "counter/b"
	if _, err := alias.Handle(ctx, "c", add{1, 10}); err == nil || !strings.Contains(err.Error(), "contain no slash") {
		t.Fatalf("want the kind refused, got %v", err)
	}
	checkStream(t, agg, "b/c", 1, 1)
}

func TestAPanickingDeciderReachesItsCaller(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		if m.(add).N == 13 {
			panic("unlucky")
		}
		return decide(s, m)
	}
	if _, err := agg.Handle(ctx, "a", add{13, 100}); err == nil || !strings.Contains(err.Error(), "unlucky") {
		t.Fatalf("want the panic as an error, got %v", err)
	}
	if h, err := agg.Handle(ctx, "a", add{1, 100}); err != nil || h.Entry.Seq != 1 {
		t.Fatalf("after a panic: %+v, %v", h, err)
	}
}

func TestACallerThatHasGivenUpIsNotRecorded(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := agg.Handle(gone, "a", add{1, 100}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	checkStream(t, agg, "a", 0, 0)
}

func TestACacheTooSmallForEveryStreamStaysCorrect(t *testing.T) {
	t.Parallel()
	agg := counterOn(ledgertest.NewStore(t, ledger.StateCacheSize(1)))
	for i := range 6 {
		for _, id := range []string{"a", "b", "c"} {
			if h, err := agg.Handle(ctx, id, add{1, 100}); err != nil || h.Result != i+1 {
				t.Fatalf("%s: %+v, %v", id, h, err)
			}
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		checkStream(t, agg, id, 6, 6)
	}
}

// A store given a byte budget does not cache a state larger than it, so every
// message to that stream reads the store first, as it would for an evicted
// stream. The same state fits the same cache when only the stream count
// bounds it.
func TestAByteBudgetEvictsAStateTooLargeForIt(t *testing.T) {
	t.Parallel()
	pool, trips := tracedPool(t, 1)
	bounded := counterOn(ledger.New(pool, ledger.RequireActor(false), ledger.StateCacheSize(10), ledger.StateCacheBytes(1)))
	roomy := counterOn(ledger.New(pool, ledger.RequireActor(false), ledger.StateCacheSize(10), ledger.StateCacheBytes(1<<20)))
	for _, agg := range []*ledger.Aggregate[counter, msg, int, event, effect]{bounded, roomy} {
		agg.StateVersion, agg.SnapshotEvery = "v1", 1
	}
	handle := func(agg *ledger.Aggregate[counter, msg, int, event, effect], id string) int64 {
		t.Helper()
		trips.reset()
		if _, err := agg.Handle(ctx, id, add{1, 100}); err != nil {
			t.Fatal(err)
		}
		return trips.trips.Load()
	}
	// The first message to each stream is read from the store, and the next
	// message to the roomy stream is the first on the cached path, which
	// prepares its statements.
	handle(bounded, "a")
	handle(roomy, "b")
	handle(roomy, "b")
	if n := handle(bounded, "a"); n != 2 {
		t.Fatalf("a state larger than the byte budget took %d round trips, want 2", n)
	}
	if n := handle(roomy, "b"); n != 1 {
		t.Fatalf("a state within the byte budget took %d round trips, want 1", n)
	}
	checkStream(t, bounded, "a", 2, 2)
	checkStream(t, roomy, "b", 3, 3)
}

// A state with a map in it, so that a caller holding one could change what
// the cache holds.
type basket struct{ Items map[string]int }

type put struct{ Item string }

type placed struct{ Item string }

var basketDecider = cairn.Decider[basket, put, int, placed, struct{}]{
	Initial: func() basket { return basket{} },
	Decide: func(s basket, m put) cairn.Decision[int, placed, struct{}] {
		return cairn.Accept[int, placed, struct{}](s.Items[m.Item]+1, []placed{{m.Item}})
	},
	Evolve: func(s basket, e placed) basket {
		items := maps.Clone(s.Items)
		if items == nil {
			items = map[string]int{}
		}
		items[e.Item]++
		return basket{Items: items}
	},
}

func newBasket(t *testing.T, store *ledger.Store) *ledger.Aggregate[basket, put, int, placed, struct{}] {
	t.Helper()
	msgs, events, effects := codec.New[put](), codec.New[placed](), codec.New[struct{}]()
	codec.Register[put](msgs, "put", 1)
	codec.Register[placed](events, "placed", 1)
	results := codec.New[int]()
	codec.Register[int](results, "count", 1)
	return &ledger.Aggregate[basket, put, int, placed, struct{}]{
		Store: store, Kind: "basket", Decider: basketDecider,
		Codecs: &codec.Set[put, int, placed, struct{}]{Messages: msgs, Results: results, Events: events, Effects: effects},
	}
}

// Two processes taking turns on one stream make every cached state stale.
// The cache notices, and reads the stream instead, so a message costs about
// the two round trips it would with no cache rather than three.
func TestAStreamSharedWithAnotherProcessCostsNoMoreThanWithoutACache(t *testing.T) {
	t.Parallel()
	agg, trips := tracedCounter(t, 1)
	other := counterOn(ledger.New(agg.Store.Pool(), ledger.RequireActor(false)))

	const n = 200
	var mine int64
	for i := range n {
		if _, err := other.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
			t.Fatal(err)
		}
		trips.reset()
		if _, err := agg.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
			t.Fatal(err)
		}
		if i >= 10 {
			mine += trips.trips.Load()
		}
	}
	if per := float64(mine) / (n - 10); per > 2.1 {
		t.Fatalf("%.2f round trips per message, want about 2", per)
	}
	checkStream(t, agg, "a", 2*n, 2*n)

	// Once the stream is this process's alone, the cache is trusted again.
	for range 80 {
		trips.reset()
		if _, err := agg.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}
	if n := trips.trips.Load(); n != 1 {
		t.Fatalf("a stream no one else writes took %d round trips, want 1", n)
	}
}

// The same, with both processes writing at once, as behind a load balancer
// that doesn't route by stream: a write by one between two of the other's is
// a matter of timing, and the cache must still cost no more than none.
func TestAStreamTwoProcessesWriteAtOnceCostsNoMoreThanWithoutACache(t *testing.T) {
	t.Parallel()
	pool, trips := tracedPool(t, 2)
	const each = 150
	var wg sync.WaitGroup
	for range 2 {
		agg := counterOn(ledger.New(pool, ledger.RequireActor(false)))
		wg.Go(func() {
			for range each {
				if _, err := agg.Handle(ctx, "a", add{1, 1 << 30}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if per := float64(trips.trips.Load()) / (2 * each); per > 2.15 {
		t.Fatalf("%.2f round trips per message, want about 2", per)
	}
	checkStream(t, counterOn(ledger.New(pool, ledger.RequireActor(false))), "a", 2*each, 2*each)
}
