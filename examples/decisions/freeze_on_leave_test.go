package decisions

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/spectest"
)

// DR5, FreezeOnSettle, states: "Options or positions change while the
// decision is not open." Its check exempts only creation, so a message that
// moves a decision out of open in the same breath as changing its options
// must be caught. This is the regression test for the gap that let that
// message survive.
//
// The trigger is aimed at the Open state rather than left to the broad
// generator: reaching the transition by chance made the test depend on rapid's
// check budget, which -short divides by five.
func TestFreezeRuleCatchesOptionsChangedWhileLeavingOpen(t *testing.T) {
	m := machine()
	m.Messages = aimed(t, Open, SetStatus{By: "ana", To: Deferred})
	m.Decider.Decide = func(s State, msg Msg) decision {
		if st, ok := msg.(SetStatus); ok && s.Status == Open && st.To == Deferred {
			return decision{
				Result: Result{Status: Deferred},
				Events: []Event{
					StatusChanged{From: Open, To: Deferred},
					OptionAdded{Option: Option{ID: "o-sneak", Title: "added on the way out"}},
				},
			}
		}
		return decide(s, msg)
	}

	msg, caught, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the freeze mutant made the check die instead of %s catching it: %v", FreezeOnSettle.ID, err)
	}
	if !caught {
		t.Fatal("a message that changed a decision's options while moving it out of open survived the spec; the freeze rule did not catch it")
	}
	if !strings.Contains(msg, FreezeOnSettle.ID+" violated") {
		t.Fatalf("%s must catch this mutant; got: %s", FreezeOnSettle.ID, firstLine(msg))
	}
	t.Logf("caught: %s", firstLine(msg))
}
