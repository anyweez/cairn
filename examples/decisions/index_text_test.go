package decisions

import (
	"testing"
	"time"
)

// The reference index is told the decision's text by an IndexReferences
// effect. The effect names the rows in the application's table that hold the
// text — the question, and the rationale once one is recorded — and the shell
// resolves them (Content.IndexedText) when it delivers the effect.
//
// Every message that changes that text must tell the index the new rows, or
// the index goes on answering with text the decision no longer names.
//
// Two messages did not. Reopening a settled decision clears the outcome, so
// the rationale leaves the text, but the reopen emitted no effect at all, and
// the index kept the cleared rationale. Editing a settled decision sent only
// the new question, dropping the rationale the state still carries. In both
// cases the text handed to the index was not the decision's text.
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
	indexed := func(d decision) (IndexReferences, bool) {
		for _, e := range d.Effects {
			if ir, ok := e.(IndexReferences); ok {
				return ir, true
			}
		}
		return IndexReferences{}, false
	}

	t.Run("reopen clears the rationale from the index", func(t *testing.T) {
		s := decided(t)
		d, after := Decider.Step(s, SetStatus{By: "ana", To: Open})
		if d.Refused() {
			t.Fatal(d.Refusal)
		}
		got, ok := indexed(d)
		if !ok {
			t.Fatalf("reopen emitted no IndexReferences; the index keeps %q and %q",
				s.Title, s.Outcome.Rationale)
		}
		if got.Title != after.Title || got.Rationale != "" {
			t.Fatalf("reopen re-indexed %+v, want %q and no rationale", got, after.Title)
		}
	})

	t.Run("edit keeps the rationale in the index", func(t *testing.T) {
		s := decided(t)
		d, after := Decider.Step(s, Edit{By: "ana", Title: "ship it now?"})
		if d.Refused() {
			t.Fatal(d.Refusal)
		}
		got, ok := indexed(d)
		if !ok {
			t.Fatal("edit emitted no IndexReferences")
		}
		if want := (IndexReferences{Decision: after.ID, Title: after.Title, Rationale: after.Outcome.Rationale}); got != want {
			t.Fatalf("edit re-indexed %+v, want %+v", got, want)
		}
	})
}
