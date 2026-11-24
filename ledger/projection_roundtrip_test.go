package ledger_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// docs/PERFORMANCE.md's cost table says `CatchUp` costs 5 round trips per batch
// of up to BatchSize entries. This catch-up's visible backlog is shorter than
// its batch, so the read that found it is also the one that shows there is
// nothing after it, and there is no further transaction to discover the log is
// empty. The batch boundary has its own test,
// TestACatchUpOfOneFullBatchCostsTheDocumentedRoundTrips.
func TestCatchUpKeepsToItsDocumentedRoundTrips(t *testing.T) {
	t.Parallel()
	agg, trips := tracedCounter(t, 1, ledger.StateCacheSize(0))
	p := ledger.Projection{Store: agg.Store, Name: "budget", BatchSize: 10,
		Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
			b.Queue(`SELECT 1`)
			return nil
		}}

	// Warm every statement the catch-up sends, including Apply's, so the
	// measurement counts round trips and not one-off statement preparation.
	if _, err := agg.Handle(ctx, "warm", add{1, 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := agg.Handle(ctx, "warm", add{1, 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}

	trips.reset()
	n, err := p.CatchUp(ctx)
	if err != nil || n != 3 {
		t.Fatalf("caught up %d entries, %v; want 3", n, err)
	}
	// One batch of up to BatchSize entries: the documented cost is 5.
	if got := trips.trips.Load(); got != 5 {
		t.Fatalf("CatchUp of one batch made %d round trips, want the documented 5", got)
	}
}

// A batch that is exactly full does have to read again to know whether more is
// visible, but that read is the next batch rather than a probe for an empty
// log. So each full batch costs the documented 5, and a catch-up that applies
// two of them costs 10.
func TestCatchUpOfSeveralFullBatchesCostsTheDocumentedRoundTrips(t *testing.T) {
	t.Parallel()
	agg, trips := tracedCounter(t, 1, ledger.StateCacheSize(0))
	const batch = 5
	p := ledger.Projection{Store: agg.Store, Name: "budget-batches", BatchSize: batch,
		Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
			b.Queue(`SELECT 1`)
			return nil
		}}

	// Warm every statement the catch-up sends, Apply's included, on a backlog
	// shorter than the batch.
	if _, err := agg.Handle(ctx, "warm", add{1, 1 << 30}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	for i := range 2 * batch {
		if _, err := agg.Handle(ctx, fmt.Sprintf("full%d", i), add{1, 1 << 30}); err != nil {
			t.Fatal(err)
		}
	}

	trips.reset()
	n, err := p.CatchUp(ctx)
	if err != nil || n != 2*batch {
		t.Fatalf("caught up %d entries, %v; want %d", n, err, 2*batch)
	}
	if got := trips.trips.Load(); got != 10 {
		t.Fatalf("CatchUp of two full batches made %d round trips, want the documented 10", got)
	}
}
