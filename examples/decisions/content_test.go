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

// An edit replaces the question, and the state then names only the new ref.
// The old row is what the edit replaced, so deleting the row the state names
// must release it too; otherwise the words an editor changed stay readable
// under the ref the ledger's Edited event still carries.
func TestErasureRemovesTheTitleAnEditReplaced(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	id, err := svc.Create(ctx, "ana", "Ship the beta?", "yes")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	oldRef := state.Title

	// The shell has no Edit method, so the test mints the replacement the way
	// the shell stores any title and hands the message to the register.
	ben := svc.Content.Member("ben")
	newRef, err := svc.store("the question", titleSlot(id), ben, "Ship the beta now?")
	must(t, err)
	_, err = svc.Register.Handle(ctx, id, Edit{By: ben, Title: newRef})
	must(t, err)

	state, err = svc.Get(ctx, id)
	must(t, err)
	if state.Title != newRef || oldRef == newRef {
		t.Fatalf("the edit did not replace the question: state names %q, the old row is %q", state.Title, oldRef)
	}

	// The row the state names, deleted the way content_test.go's erasure does.
	svc.Content.Delete(state.Title)

	if got := svc.Content.Text(oldRef); got != "" {
		t.Fatalf("after erasing the question, the register's table still holds the title the edit replaced, %q", got)
	}
	if got := svc.Content.Text(newRef); got != "" {
		t.Fatalf("the question the state names survived deletion: %q", got)
	}
}

// A reopen clears the outcome and keeps the ref in its Reopened event, so the
// state no longer names the rationale it cleared. It is still the member's
// words, and forgetting them must reach it: nothing in the current state does.
func TestErasureRemovesARationaleAReopenCleared(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	const member = "ana.sorensen@example.com"
	id, err := svc.Create(ctx, member, "Ship the beta?", "yes")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	must(t, svc.RecordOutcome(ctx, id, member, state.Options[0].ID, "the bugs are cosmetic"))

	state, err = svc.Get(ctx, id)
	must(t, err)
	if state.Outcome == nil || state.Outcome.Rationale == "" {
		t.Fatal("the decision recorded no rationale, so this test proves nothing")
	}
	cleared := state.Outcome.Rationale

	must(t, svc.SetStatus(ctx, id, member, Open))
	state, err = svc.Get(ctx, id)
	must(t, err)
	if state.Outcome != nil {
		t.Fatalf("the reopen did not clear the outcome: %+v", state.Outcome)
	}

	ana := svc.Content.Member(member)
	svc.Content.Delete(state.Title, state.Options[0].Title)
	svc.Content.Forget(ana)

	if got := svc.Content.Text(cleared); got != "" {
		t.Fatalf("after erasing the member, the register's table still holds the rationale the reopen cleared, %q", got)
	}

	divergences, err := svc.Register.VerifyAll(ctx)
	must(t, err)
	if len(divergences) != 0 {
		t.Fatalf("divergences after erasure: %v", divergences)
	}
}
