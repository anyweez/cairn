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

// A decision's own ID (Create.ID, kept as State.ID) and the ledger stream it
// lives on are two identifiers for one entity, and nothing ties them together.
// The register's Supersede guard refuses a decision that names itself
// (m.With == s.ID), but Service.Supersede sets m.With to the replacement's
// *stream* while s.ID is the decision's ID. When a caller records a Create
// whose ID is not its stream (the public Aggregate.Handle API allows it), the
// guard compares a stream to an ID and a decision can supersede itself: the
// state ends up Superseded, naming its own stream as the replacement, and
// Verify re-decides the stored message to the same corrupt state.
func TestADecisionCannotSupersedeItself(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	// The decision's own ID is "state-1"; its stream is "stream-1".
	if _, err := svc.Register.Handle(ctx, "stream-1", Create{
		ID: "state-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(),
		Title: "Ship it?", Options: []Option{{ID: "o1", Title: "yes"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordOutcome(ctx, "stream-1", "ana", "o1", "because"); err != nil {
		t.Fatal(err)
	}

	// Superseding a decision with itself must be refused.
	err := svc.Supersede(ctx, "stream-1", "stream-1", "ana")
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a decision superseded itself; want a refusal, got %v", err)
	}

	after, err := svc.Get(ctx, "stream-1")
	if err != nil {
		t.Fatal(err)
	}
	if after.Status == Superseded && after.SupersededBy == "stream-1" {
		t.Fatalf("the decision is superseded by its own stream: %+v", after)
	}
}
