package decisions

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/spectest"
)

// DR5, FreezeOnSettle, states: "Options or positions change while the
// decision is not open." Its check only looks at the state *before* the
// message:
//
//	if before.Status == Open || before.Status == Uncreated {
//		return spec.NA
//	}
//
// A message that moves a decision out of open in the same breath as changing
// its options is therefore never checked: before is open, so the check
// answers NA. This mutant defers a decision from open and adds an option in
// the same message; DR1 allows the status edge, and no other rule looks at
// the option change, so the mutant survives.
//
// The rule should exempt only creation (Uncreated -> open) and otherwise
// require options and positions to be unchanged whenever either the before or
// the after state is not open.
func TestFreezeRuleCatchesOptionsChangedWhileLeavingOpen(t *testing.T) {
	m := machine()
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

	msg, caught := spectest.Fails(t, m.Check)
	if !caught {
		t.Fatal("a message that changed a decision's options while moving it out of open survived the spec; the freeze rule did not catch it")
	}
	if !strings.Contains(msg, FreezeOnSettle.ID+" violated") {
		t.Fatalf("%s must catch this mutant; got: %s", FreezeOnSettle.ID, firstLine(msg))
	}
	t.Logf("caught: %s", firstLine(msg))
}
