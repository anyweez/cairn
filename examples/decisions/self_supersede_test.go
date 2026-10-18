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
// (m.With == s.ID). Service.Supersede used to send the replacement's *stream*
// as With while s.ID is the decision's ID, so when a caller recorded a Create
// whose ID was not its stream (the public Aggregate.Handle API allows it) the
// guard compared a stream to an ID and the decision superseded itself: the
// state ended up Superseded, naming its own stream as the replacement, and
// Verify re-decided the stored message to the same corrupt state. This is that
// regression; the service now sends the replacement's decision ID.
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

// A decision is named by its own ID, not the stream it happens to sit on, so a
// supersede records the replacement's decision ID even when neither decision's
// ID is its stream. DR3 and Verify then compare like with like.
func TestSupersedeNamesTheReplacementByItsID(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))
	at := time.Unix(1_700_000_000, 0).UTC()

	create := func(stream, id string) {
		t.Helper()
		if _, err := svc.Register.Handle(ctx, stream, Create{ID: id, By: "ana", At: at,
			Title: "Ship it?", Options: []Option{{ID: "o1", Title: "yes"}}}); err != nil {
			t.Fatal(err)
		}
		if err := svc.RecordOutcome(ctx, stream, "ana", "o1", "because"); err != nil {
			t.Fatal(err)
		}
	}
	create("stream-1", "state-1")
	create("stream-2", "state-2")

	must(t, svc.Supersede(ctx, "stream-1", "stream-2", "ana"))

	after, err := svc.Get(ctx, "stream-1")
	must(t, err)
	if after.Status != Superseded || after.SupersededBy != "state-2" {
		t.Fatalf("superseded decision names %q, want the replacement's ID state-2: %+v", after.SupersededBy, after)
	}

	// The stored message re-decides to the same state: Verify sees no divergence.
	divergences, err := svc.Register.VerifyAll(ctx)
	must(t, err)
	if len(divergences) != 0 {
		t.Fatalf("divergences: %v", divergences)
	}
}
