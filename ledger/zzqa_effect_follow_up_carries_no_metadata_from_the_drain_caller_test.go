package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// A message recorded while carrying out an effect is attributed to the entry
// that produced the effect: its actor, its flow, and the delivery as cause
// (Dispatcher.Handle documents exactly that list). Metadata a *third* request
// carries is not part of it, but the delivery's context is derived from
// whichever context ran the drain, so a drain triggered from a request of its
// own leaves that request's Extra — and its OccurredAt — on the follow-up
// entry. The entry then records a tenant, an IP or a time that belongs to a
// request that has nothing to do with it.
func TestAFollowUpMessageDoesNotInheritTheDrainCallersMetadata(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	producer := ledger.ContextWithMeta(ctx, ledger.Meta{
		Actor: "u_ana", Via: "ui", CorrelationID: "req-1",
		Extra: map[string]any{"tenant": "t-1"},
	})
	if _, err := agg.Handle(producer, "a", add{2, 10}); err != nil {
		t.Fatal(err)
	}

	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
		Handle: func(hctx context.Context, dl ledger.Delivery[effect]) error {
			_, err := agg.Handle(hctx, "audit", add{1, 1000})
			return err
		},
	}
	// The drain is run from an operator's own request: its own actor, flow,
	// Extra and claimed time.
	drain := ledger.ContextWithMeta(ctx, ledger.Meta{
		Actor: "u_ops", Via: "admin", CorrelationID: "req-ops",
		OccurredAt: time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC),
		Extra:      map[string]any{"ip": "10.0.0.9"},
	})
	if n, err := d.DrainOnce(drain); err != nil || n != 1 {
		t.Fatalf("drained %d, %v", n, err)
	}

	entries, err := agg.Store.ReadStream(ctx, agg.Stream("audit"), 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries %+v, %v", entries, err)
	}
	got := entries[0].Meta
	if _, ok := got.Extra["ip"]; ok {
		t.Fatalf("the follow-up entry carries the drain caller's Extra: %+v", got)
	}
	if !got.OccurredAt.IsZero() {
		t.Fatalf("the follow-up entry carries the drain caller's OccurredAt: %+v", got)
	}
	// What the delivery does contribute stays: the producing entry's actor and
	// flow, through the outbox, caused by the delivery.
	if got.Actor != "u_ana" || got.Via != "outbox" || got.CorrelationID != "req-1" || got.CausationID == "" {
		t.Fatalf("the follow-up entry lost the delivery's attribution: %+v", got)
	}
}
