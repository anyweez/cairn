package decisions

import (
	"context"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The register keeps the words in the application's own mutable table and the
// ledger records only the Ref that names the row, so nothing but the shell can
// keep a person's text as they wrote it. The shell trims it: Service.store and
// Content.put both run strings.TrimSpace over the text before storing it
// (`examples/decisions/service.go`, `examples/decisions/content.go`), so a
// question, an option's title, a position's body or an outcome's rationale
// comes back from Content.Text with its leading and trailing whitespace
// removed — a first line indented as a code block or a list loses it, and a
// body that starts with a blank line loses it.
//
// The trim is where blank text is detected, and that part is wanted: text that
// is only whitespace must still be stored under no ref so the decider refuses
// the message. It is the storing of the trimmed value that is not: the same
// repository refuses text whose bytes it cannot keep for exactly this reason
// ("the ledger would hold text the caller never wrote", `cairn.StorableText`,
// `text_fidelity_test.go`), and the decider's own trims are of Refs, opaque
// ids that have no whitespace — the trim here is what the message used to get
// when the text travelled in it.
func TestTheRegisterKeepsTheWhitespaceACallerSent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	const (
		question  = "  Ship the beta?\n"
		option    = " yes\n"
		body      = "  ana's team can support it\n\tfrom the 5th\n"
		rationale = "\n  the bugs are cosmetic\n"
	)

	id, err := svc.Create(ctx, "ana", question, option)
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	// The premise: the message was recorded, so a missing row below is the
	// text not having been kept rather than the request not having happened.
	if state.Status != Open || state.Title == "" || len(state.Options) != 1 {
		t.Fatalf("the Create was not recorded, so this test proves nothing: %+v", state)
	}
	if got := svc.Content.Text(state.Title); got != question {
		t.Errorf("the question is stored as %q, but the caller sent %q", got, question)
	}
	if got := svc.Content.Text(state.Options[0].Title); got != option {
		t.Errorf("the option's title is stored as %q, but the caller sent %q", got, option)
	}

	must(t, svc.SavePosition(ctx, id, "ana", state.Options[0].ID, body))
	state, err = svc.Get(ctx, id)
	must(t, err)
	position, ok := state.Positions[svc.Content.Member("ana")]
	if !ok || position.Body == "" {
		t.Fatalf("the SavePosition was not recorded, so this test proves nothing: %+v", state.Positions)
	}
	if got := svc.Content.Text(position.Body); got != body {
		t.Errorf("the position's body is stored as %q, but the caller sent %q", got, body)
	}

	must(t, svc.RecordOutcome(ctx, id, "ben", state.Options[0].ID, rationale))
	state, err = svc.Get(ctx, id)
	must(t, err)
	if state.Outcome == nil || state.Outcome.Rationale == "" {
		t.Fatalf("the RecordOutcome was not recorded, so this test proves nothing: %+v", state.Outcome)
	}
	if got := svc.Content.Text(state.Outcome.Rationale); got != rationale {
		t.Errorf("the outcome's rationale is stored as %q, but the caller sent %q", got, rationale)
	}
}

// The same for a row stored through Content's own Put, which is the table's
// public entry point for the register's words: it trims too, and its own
// comment promises only that text which is *only* whitespace is stored under
// no ref.
func TestContentKeepsTheWhitespaceACallerSent(t *testing.T) {
	content := NewContent()
	const text = "  one\n\n  two  "
	ref := content.Put(text)
	if ref == "" {
		t.Fatal("Put stored no row for text that is not blank")
	}
	if got := content.Text(ref); got != text {
		t.Errorf("Put stored %q, but the caller sent %q", got, text)
	}
}
