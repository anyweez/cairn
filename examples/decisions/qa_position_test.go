package decisions

import (
	"testing"
	"time"
)

// A position is filed by the member the message names. A message that names no
// member must be refused, not filed under nobody: the shell cannot stamp a
// member the way it stamps the entry's actor, and a position under "" is
// unreachable by Acknowledge and invisible to the member who sent it.
func TestSavePositionNeedsAMember(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	state := Decider.Initial()
	var d decision
	if d, state = Decider.Step(state, Create{ID: "d-1", By: "ana", At: at, Title: "ship it?", Options: []Option{{ID: "o1", Title: "yes"}}}); d.Refused() {
		t.Fatalf("Create refused: %v", d.Refusal)
	}
	d, after := Decider.Step(state, SavePosition{By: "", Body: "yes"})
	if !d.Refused() {
		t.Fatalf("SavePosition with an empty member was accepted, filing it under %q", "")
	}
	_ = after
}
