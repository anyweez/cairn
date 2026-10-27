package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// Delivery.Meta is documented as "the metadata of the entry that produced the
// effect". That includes the producer's OccurredAt and the Extra fields; a
// handler that keys its work on either one gets nothing today.
func TestADeliveryCarriesTheWholeMetaOfTheEntryThatProducedIt(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	occurred := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	request := ledger.ContextWithMeta(ctx, ledger.Meta{
		Actor: "u_ana", Via: "ui", CorrelationID: "req-1",
		OccurredAt: occurred, Extra: map[string]any{"tenant": "t-1"},
	})
	if _, err := agg.Handle(request, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}

	var delivered ledger.Delivery[effect]
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
		Handle: func(_ context.Context, dl ledger.Delivery[effect]) error {
			delivered = dl
			return nil
		},
	}
	if n, err := d.DrainOnce(ctx); err != nil || n != 1 {
		t.Fatalf("drained %d, %v", n, err)
	}

	if !delivered.Meta.OccurredAt.Equal(occurred) {
		t.Errorf("Delivery.Meta.OccurredAt = %v, want the entry's %v", delivered.Meta.OccurredAt, occurred)
	}
	if got := delivered.Meta.Extra["tenant"]; got != "t-1" {
		t.Errorf("Delivery.Meta.Extra = %v, want the entry's tenant t-1", delivered.Meta.Extra)
	}
}
