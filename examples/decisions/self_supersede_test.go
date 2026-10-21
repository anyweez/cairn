package decisions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A decision is identified by the stream it is recorded on. The register asks
// the ledger to hold a Create to the stream its ID names (Aggregate.StreamOf),
// so the two identifiers cannot disagree. A caller recording a Create through
// the public Aggregate.Handle with an ID of its own is refused, and nothing is
// recorded — not even the refusal.
func TestACreateMustBeRecordedOnTheStreamItNames(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))
	create := Create{ID: "stream-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(),
		Title: "Ship it?", Options: []Option{{ID: "o1", Title: "yes"}}}

	_, err := svc.Register.Handle(ctx, "stream-2", create)
	if !errors.Is(err, ledger.ErrStreamMismatch) {
		t.Fatalf("a Create naming stream-1 was recorded on stream-2: %v", err)
	}
	if streams, err := svc.Register.Store.Streams(ctx, "decision"); err != nil || len(streams) != 0 {
		t.Fatalf("a refused Create left a stream behind: %v, %v", streams, err)
	}

	// Recorded on the stream it names, the same Create is accepted.
	if _, err := svc.Register.Handle(ctx, "stream-1", create); err != nil {
		t.Fatal(err)
	}
	state, err := svc.Get(ctx, "stream-1")
	must(t, err)
	if state.ID != "stream-1" || state.Status != Open {
		t.Fatalf("created %+v, want an open decision with ID stream-1", state)
	}
}

// The self-supersede guard compares the replacement's name to the decision's
// own ID. Since a decision's stream is its ID, superseding it with itself is
// refused however the decision was recorded.
func TestADecisionCannotSupersedeItself(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	id, err := svc.Create(ctx, "ana", "Ship it?", "yes")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	must(t, svc.RecordOutcome(ctx, id, "ana", state.Options[0].ID, "because"))

	err = svc.Supersede(ctx, id, id, "ana")
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a decision superseded itself; want a refusal, got %v", err)
	}

	after, err := svc.Get(ctx, id)
	must(t, err)
	if after.Status != Decided || after.SupersededBy != "" {
		t.Fatalf("the refused supersede changed the decision: %+v", after)
	}
}

// Supersede records the replacement by the ID it is recorded on, so the
// superseded decision names a stream: Get(SupersededBy) reads the replacement
// rather than missing it.
func TestSupersedeNamesTheReplacementByItsStream(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	decided := func(title string) string {
		t.Helper()
		id, err := svc.Create(ctx, "ana", title, "yes")
		must(t, err)
		state, err := svc.Get(ctx, id)
		must(t, err)
		must(t, svc.RecordOutcome(ctx, id, "ana", state.Options[0].ID, "because"))
		return id
	}
	first, second := decided("Ship the beta?"), decided("Ship the beta to everyone?")

	must(t, svc.Supersede(ctx, first, second, "ana"))

	after, err := svc.Get(ctx, first)
	must(t, err)
	if after.Status != Superseded || after.SupersededBy != second {
		t.Fatalf("superseded decision names %q, want the replacement %s: %+v", after.SupersededBy, second, after)
	}
	replacement, err := svc.Get(ctx, after.SupersededBy)
	must(t, err)
	if replacement.Status != Decided {
		t.Fatalf("reading SupersededBy got %+v, want the decided replacement", replacement)
	}

	// The stored message re-decides to the same state: Verify sees no divergence.
	divergences, err := svc.Register.VerifyAll(ctx)
	must(t, err)
	if len(divergences) != 0 {
		t.Fatalf("divergences: %v", divergences)
	}
}
