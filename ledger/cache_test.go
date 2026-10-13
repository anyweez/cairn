package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

func cachedCounter(t *testing.T) *ledger.Aggregate[counter, msg, int, event, effect] {
	t.Helper()
	agg := newCounter(t)
	agg.Cache = ledger.NewStateCache(100)
	return agg
}

// handleAll sends msgs to one stream at once, and returns each one's outcome
// in the order given.
func handleAll(agg *ledger.Aggregate[counter, msg, int, event, effect], id string, msgs []msg, opts ...ledger.Option) ([]ledger.Handled[counter, int], []error) {
	hs := make([]ledger.Handled[counter, int], len(msgs))
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
	stored := *agg
	stored.Cache = nil
	loaded, err := stored.Load(ctx, id)
	if err != nil || loaded.State.Total != want {
		t.Fatalf("stored total %d, %v; want %d", loaded.State.Total, err, want)
	}
}

func TestACachedStreamCostsOneRoundTrip(t *testing.T) {
	agg, trips := tracedCounter(t, 1)
	agg.Cache = ledger.NewStateCache(10)
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
	agg := cachedCounter(t)
	other := *agg
	other.Cache = nil

	if _, err := agg.Handle(ctx, "a", add{3, 5}); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Handle(ctx, "a", add{2, 5}); err != nil {
		t.Fatal(err)
	}
	// Cached, a total of 3 would accept this; stored, it is 5, which refuses.
	h, err := agg.Handle(ctx, "a", add{1, 5})
	if !errors.Is(err, &cairn.Refusal{Code: "over_cap"}) || h.Entry.Seq != 3 || h.State.Total != 5 {
		t.Fatalf("decided against stale state: %+v, %v", h, err)
	}
	if h, err := agg.Handle(ctx, "a", add{0, 5}); err != nil || h.Entry.Seq != 4 || h.State.Total != 5 {
		t.Fatalf("after catching up: %+v, %v", h, err)
	}
	checkStream(t, agg, "a", 4, 5)
}

func TestMessagesArrivingTogetherShareATransaction(t *testing.T) {
	agg := cachedCounter(t)
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}
	const n = 60
	hs, errs := handleAll(agg, "a", slices.Repeat([]msg{add{1, 1000}}, n))

	var totals []int
	for i := range hs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		totals = append(totals, hs[i].State.Total)
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
	agg := cachedCounter(t)
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
	agg := cachedCounter(t)
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

	// A retry that lost its metadata still gets its first answer.
	agg.Store = ledger.New(agg.Store.Pool())
	if h, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k")); err != nil || !h.Duplicate {
		t.Fatalf("a retry without an actor: %+v, %v", h, err)
	}
}

// When one message's view fails in the database, it spoils the transaction
// it shares with others. The others are then recorded one by one, and only
// the message whose view fails is refused.
func TestOnlyTheMessageWhoseViewFailsIsLost(t *testing.T) {
	agg := cachedCounter(t)
	view := totalsView(t, agg)
	agg.Views = []ledger.View[counter]{{Name: "counter_totals", Write: func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
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
}

func TestAPanickingDeciderReachesItsCaller(t *testing.T) {
	agg := cachedCounter(t)
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
	agg := cachedCounter(t)
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := agg.Handle(gone, "a", add{1, 100}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	checkStream(t, agg, "a", 0, 0)
}

func TestACacheTooSmallForEveryStreamStaysCorrect(t *testing.T) {
	agg := newCounter(t)
	agg.Cache = ledger.NewStateCache(1)
	for i := range 6 {
		for _, id := range []string{"a", "b", "c"} {
			if h, err := agg.Handle(ctx, id, add{1, 100}); err != nil || h.State.Total != i+1 {
				t.Fatalf("%s: %+v, %v", id, h, err)
			}
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		checkStream(t, agg, id, 6, 6)
	}
}
