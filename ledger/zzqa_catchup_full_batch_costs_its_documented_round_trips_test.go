package ledger_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// docs/PERFORMANCE.md prices `CatchUp` at five round trips "per batch of up to
// `BatchSize` entries", and the fix for t2570 made a catch-up return after a
// *short* batch instead of reading the log once more to discover it is empty.
// The case that fix leaves: a batch of exactly `BatchSize` entries is not
// short, so the catch-up opens one more transaction (BEGIN, read the
// checkpoint, read the log, COMMIT) to find nothing, and a catch-up that
// applies one full batch costs nine round trips rather than the documented
// five.
func TestACatchUpOfOneFullBatchCostsTheDocumentedRoundTrips(t *testing.T) {
	t.Parallel()
	agg, trips := tracedCounter(t, 1, ledger.StateCacheSize(0))
	const batch = 5
	p := ledger.Projection{Store: agg.Store, Name: "full-batch", BatchSize: batch,
		Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
			b.Queue(`SELECT 1`)
			return nil
		}}

	// Warm every statement the catch-up sends, including Apply's, with a
	// backlog that does not end on the boundary.
	for i := range batch - 1 {
		if _, err := agg.Handle(ctx, fmt.Sprintf("warm%d", i), add{1, 1000}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}

	// Exactly one full batch of new entries, and nothing after it.
	for i := range batch {
		if _, err := agg.Handle(ctx, fmt.Sprintf("full%d", i), add{1, 1000}); err != nil {
			t.Fatal(err)
		}
	}

	trips.reset()
	n, err := p.CatchUp(ctx)
	if err != nil || n != batch {
		t.Fatalf("caught up %d entries, %v; want %d", n, err, batch)
	}
	if got := trips.trips.Load(); got != 5 {
		t.Fatalf("a catch-up of one full batch made %d round trips, want the documented 5", got)
	}
}
