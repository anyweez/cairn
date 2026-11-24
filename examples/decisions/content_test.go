package decisions

import (
	"context"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// Erasing a person is an ordinary DELETE in the application's own table: their
// name, the positions they wrote, and the link from the name to their id. The
// ledger never held any of it, so the history still loads and verifies — the
// promise docs/REDACTION.md makes, exercised end to end.
func TestErasingTheWordsLeavesTheHistoryVerifiable(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	const (
		member    = "ana.sorensen@example.com"
		question  = "Should we ship the beta to Ana's team?"
		body      = "Ana says her team can support it after the holidays"
		rationale = "Ana's team has the capacity, so we ship on Friday"
	)
	id, err := svc.Create(ctx, member, question, "ship it")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	must(t, svc.SavePosition(ctx, id, member, state.Options[0].ID, body))
	must(t, svc.RecordOutcome(ctx, id, member, state.Options[0].ID, rationale))

	ana := svc.Content.Member(member)
	state, err = svc.Get(ctx, id)
	must(t, err)
	svc.Content.Delete(state.Title, state.Positions[ana].Body, state.Outcome.Rationale,
		state.Options[0].Title)
	svc.Content.Forget(ana)

	if got := svc.Content.Text(state.Title); got != "" {
		t.Fatalf("the question survived deletion: %q", got)
	}
	if got := svc.Content.Name(ana); got != "" {
		t.Fatalf("the name survived deletion: %q", got)
	}

	// The ledger keeps only the refs it always had, and a decision whose words
	// are gone still re-decides to the state it stored.
	divergences, err := svc.Register.VerifyAll(ctx)
	must(t, err)
	if len(divergences) != 0 {
		t.Fatalf("divergences: %v", divergences)
	}
	loaded, err := svc.Get(ctx, id)
	must(t, err)
	if loaded.Status != Decided || loaded.Outcome == nil {
		t.Fatalf("the decision no longer reads back: %+v", loaded)
	}
}
