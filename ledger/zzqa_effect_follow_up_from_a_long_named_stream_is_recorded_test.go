package ledger_test

import (
	"context"
	"strings"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A stream name may be up to 1024 bytes: that is the bound validNewStream
// applies when an append creates the stream. An entry with no correlation ID
// starts a flow, and a follow-up message recorded from its effect handler
// inherits a correlation ID derived from the producing stream and seq
// (Delivery.caused, ledger/outbox.go). For a stream name near the bound that
// derived value is longer than the ledger's own 1024-byte bound on the
// indexed correlation ID, so the ledger refuses the follow-up with an error
// naming metadata the handler never set.
func TestAnEffectFollowUpFromALongNamedStreamIsRecorded(t *testing.T) {
	t.Parallel()
	store := ledgertest.NewStore(t, ledger.RequireActor(false), ledger.StateCacheSize(0))
	agg := counterOn(store)
	ctx := context.Background()

	// The longest stream name the ledger accepts when it creates a stream.
	name := strings.Repeat("s", 1024-len("counter/"))
	producing := agg.Stream(name)
	if got := len(producing.String()); got != 1024 {
		t.Fatalf("the fixture's stream name is %d bytes, want the 1024-byte bound", got)
	}
	// No correlation ID, so the delivery derives one from the stream and seq.
	if _, err := agg.Handle(ctx, name, add{1, 100}); err != nil {
		t.Fatalf("recording on a %d-byte stream: %v", len(producing.String()), err)
	}

	// A handler records a follow-up, which the delivery's metadata is meant to
	// make possible: the actor, the flow and the cause come from the entry.
	var followUpErr error
	d := &ledger.Dispatcher[effect]{
		Store: store, Kind: agg.Kind, Effects: agg.Codecs.Effects,
		Handle: func(ctx context.Context, _ ledger.Delivery[effect]) error {
			_, followUpErr = agg.Handle(ctx, "follow-up", add{1, 100})
			return followUpErr
		},
	}
	if _, err := d.DrainOnce(ctx); err != nil {
		t.Fatalf("draining: %v", err)
	}

	// The follow-up names no actor of its own, so ErrNoActor is not expected
	// here: RequireActor is off. Any other error means the follow-up the
	// handler recorded never reached the ledger.
	entries, err := store.ReadStream(ctx, agg.Stream("follow-up"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the effect handler's follow-up was not recorded: %v", followUpErr)
	}
	// The entry that produced the effect had no correlation ID, so the flow
	// the follow-up joins starts there; the derived value must still be one
	// the ledger can store.
	if entries[0].Meta.CorrelationID == "" {
		t.Fatal("the follow-up did not join the flow the effect started")
	}
}

// The same follow-up from an ordinary stream still joins the flow derived from
// the entry that produced the effect, which is the behaviour the ledger
// documents today. A fix for the long stream must not drop it for every
// stream.
func TestAnEffectFollowUpFromAnOrdinaryStreamJoinsTheDerivedFlow(t *testing.T) {
	t.Parallel()
	store := ledgertest.NewStore(t, ledger.RequireActor(false), ledger.StateCacheSize(0))
	agg := counterOn(store)
	ctx := context.Background()
	if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}

	var followUpErr error
	d := &ledger.Dispatcher[effect]{
		Store: store, Kind: agg.Kind, Effects: agg.Codecs.Effects,
		Handle: func(ctx context.Context, _ ledger.Delivery[effect]) error {
			_, followUpErr = agg.Handle(ctx, "follow-up", add{1, 100})
			return followUpErr
		},
	}
	if _, err := d.DrainOnce(ctx); err != nil {
		t.Fatalf("draining: %v", err)
	}
	entries, err := store.ReadStream(ctx, agg.Stream("follow-up"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the effect handler's follow-up was not recorded: %v", followUpErr)
	}
	if want := "counter/a#1"; entries[0].Meta.CorrelationID != want {
		t.Fatalf("follow-up joined flow %q, want %q", entries[0].Meta.CorrelationID, want)
	}
	if want := "counter/a#1.0"; entries[0].Meta.CausationID != want {
		t.Fatalf("follow-up was caused by %q, want %q", entries[0].Meta.CausationID, want)
	}
}
