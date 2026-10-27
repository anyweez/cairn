package ledger_test

import (
	"context"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// docs/PERFORMANCE.md's cost table says `CatchUp` costs 5 round trips per batch
// of up to BatchSize entries. A catch-up that applies one batch actually makes
// nine: the batch's own five, plus a whole extra transaction (BEGIN, read the
// checkpoint, read the log, COMMIT) that only discovers the log is empty.
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
