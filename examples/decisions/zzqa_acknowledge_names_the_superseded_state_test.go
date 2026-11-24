package decisions

import (
	"strings"
	"testing"
	"time"
)

// A member answers a recorded outcome with Acknowledge. The answer is refused
// unless the decision is decided, and one of the states it can be in instead is
// superseded — which still carries the outcome it recorded, and names the
// decision that replaced it. The refusal for that state says the decision "has
// not been recorded yet", which is the opposite of what happened: the caller is
// told to look for a missing decision when the one in front of them was
// recorded and then replaced.
//
// The two states are not the same mistake by the caller, so they must not get
// the same answer: a decision that was never recorded and one that was recorded
// and superseded are told apart by the refusal's text.
func TestAcknowledgeRefusalNamesTheSupersededState(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	step := func(state State, m Msg) State {
		t.Helper()
		d, next := Decider.Step(state, m)
		if d.Refused() {
			t.Fatalf("%T refused: %v", m, d.Refusal)
		}
		return next
	}
	create := func(id string) Msg {
		return Create{ID: id, By: "ana", At: at, Title: "ship it?",
			Options: []Option{{ID: "o1", Title: "yes"}}}
	}
	outcome := RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "because"}

	// An open decision with ben's position: no outcome has been recorded yet.
	open := step(step(Decider.Initial(), create("d-1")),
		SavePosition{By: "ben", Option: "o1", Body: "yes"})

	// A decision ben positioned on, recorded, and then superseded by d-2.
	first := step(step(open, outcome), Supersede{By: "ana", With: "d-2", WithStatus: Decided})
	if first.Status != Superseded || first.Outcome == nil || first.SupersededBy != "d-2" {
		t.Fatalf("setup did not produce a superseded decision carrying its outcome: %+v", first)
	}

	answer := func(state State) string {
		t.Helper()
		d := Decider.Decide(state, Acknowledge{By: "ben", Committed: true})
		if !d.Refused() {
			t.Fatalf("acknowledging a %s decision was accepted; setup is wrong", state.Status)
		}
		if d.Refusal.Code == "" {
			t.Fatal("refusal carries no code")
		}
		return d.Refusal.Message
	}

	unrecorded := answer(open)
	superseded := answer(first)

	if strings.Contains(superseded, "not been recorded") {
		t.Fatalf("refusing an answer to a superseded decision says it %q, but the decision carries the outcome %+v it recorded and names %s as its replacement",
			superseded, *first.Outcome, first.SupersededBy)
	}
	if superseded == unrecorded {
		t.Fatalf("a superseded decision and one that was never recorded get the same refusal %q, so the caller cannot tell a decision it must look for from the one that was recorded and replaced", superseded)
	}
}
