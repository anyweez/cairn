package decisions

import (
	"testing"
	"time"
)

// The reference index is told the decision's text by an IndexReferences
// effect: its title, and once an outcome is recorded, its rationale. Every
// message that changes that text must tell the index the new text, or the
// index goes on answering with text the decision no longer has.
//
// Two messages do not. Reopening a settled decision clears the outcome, so
// the rationale leaves the text, but the reopen emits no effect at all, and
// the index keeps the cleared rationale. Editing a settled decision sends
// only the new title, dropping the rationale the state still carries. In both
// cases the text handed to the index is not the decision's text.
func TestIndexTextFollowsTheDecision(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()

	decided := func(t *testing.T) State {
		t.Helper()
		s := Decider.Initial()
		for _, m := range []Msg{
			Create{ID: "d-1", By: "ana", At: at, Title: "ship it?",
				Options: []Option{{ID: "o1", Title: "yes"}}},
			RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "the bugs are cosmetic"},
		} {
			var d decision
			if d, s = Decider.Step(s, m); d.Refused() {
				t.Fatalf("%T refused: %v", m, d.Refusal)
			}
		}
		return s
	}
	text := func(d decision) (string, bool) {
		for _, e := range d.Effects {
			if ir, ok := e.(IndexReferences); ok {
				return ir.Text, true
			}
		}
		return "", false
	}

	t.Run("reopen clears the rationale from the index", func(t *testing.T) {
		s := decided(t)
		d, after := Decider.Step(s, SetStatus{By: "ana", To: Open})
		if d.Refused() {
			t.Fatal(d.Refusal)
		}
		got, ok := text(d)
		if !ok {
			t.Fatalf("reopen emitted no IndexReferences; the index keeps %q",
				s.Title+"\n"+s.Outcome.Rationale)
		}
		if want := after.Title; got != want {
			t.Fatalf("reopen re-indexed %q, want %q", got, want)
		}
	})

	t.Run("edit keeps the rationale in the index", func(t *testing.T) {
		s := decided(t)
		d, after := Decider.Step(s, Edit{By: "ana", Title: "ship it now?"})
		if d.Refused() {
			t.Fatal(d.Refusal)
		}
		got, ok := text(d)
		if !ok {
			t.Fatal("edit emitted no IndexReferences")
		}
		if want := after.Title + "\n" + after.Outcome.Rationale; got != want {
			t.Fatalf("edit re-indexed %q, want %q", got, want)
		}
	})
}
