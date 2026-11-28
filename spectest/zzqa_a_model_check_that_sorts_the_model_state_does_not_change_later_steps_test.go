// A refinement model's own state is handed to its callbacks too. Agree is the
// natural place to sort a slice so two states can be compared as sets, and the
// checker promises a callback that writes into the value it is given cannot
// change the run. Here the model's Agree sorts the model state it is given;
// that must not change what the model's Step computes for the next message.

package spectest_test

import (
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

type zzqaOrderState struct{ Items []int }
type zzqaOrderMsg struct{ N int }
type zzqaOrderEvent struct{ N int }

var zzqaOrderRefines = spec.Rule{ID: "ZR1", Kind: spec.Refines, Status: spec.Established,
	Statement: "The order model's next first item differs from the machine's."}

// zzqaOrderModel is the reference model: it appends to the same list and
// returns the list's first item. Agree compares the lists as multisets, which
// is what sorts them.
func zzqaOrderModel(sortModelInPlace bool) spectest.Refinement[zzqaOrderState, zzqaOrderMsg, int] {
	return spectest.Refines(spectest.Model[zzqaOrderState, zzqaOrderState, zzqaOrderMsg, int]{
		Rule:    zzqaOrderRefines,
		Initial: func() zzqaOrderState { return zzqaOrderState{} },
		Step: func(s zzqaOrderState, m zzqaOrderMsg) (zzqaOrderState, int, *cairn.Refusal) {
			next := zzqaOrderState{Items: append(slices.Clone(s.Items), m.N)}
			return next, next.Items[0], nil
		},
		Agree: func(impl zzqaOrderState, model zzqaOrderState) spec.Verdict {
			slices.Sort(impl.Items)
			modelItems := model.Items
			if !sortModelInPlace {
				modelItems = slices.Clone(model.Items)
			}
			slices.Sort(modelItems)
			return spec.Check(slices.Equal(impl.Items, modelItems), "machine %v, model %v", impl.Items, modelItems)
		},
	})
}

func zzqaOrderMachine(sortModelInPlace bool) spectest.Machine[zzqaOrderState, zzqaOrderMsg, int, zzqaOrderEvent, struct{}] {
	return spectest.Machine[zzqaOrderState, zzqaOrderMsg, int, zzqaOrderEvent, struct{}]{
		Decider: cairn.Decider[zzqaOrderState, zzqaOrderMsg, int, zzqaOrderEvent, struct{}]{
			Initial: func() zzqaOrderState { return zzqaOrderState{} },
			Decide: func(s zzqaOrderState, m zzqaOrderMsg) cairn.Decision[int, zzqaOrderEvent, struct{}] {
				first := m.N
				if len(s.Items) > 0 {
					first = s.Items[0]
				}
				return cairn.Accept[int, zzqaOrderEvent, struct{}](first, []zzqaOrderEvent{{N: m.N}})
			},
			Evolve: func(s zzqaOrderState, e zzqaOrderEvent) zzqaOrderState {
				return zzqaOrderState{Items: append(slices.Clone(s.Items), e.N)}
			},
		},
		Messages: func(_ *rapid.T, s zzqaOrderState) zzqaOrderMsg {
			switch len(s.Items) {
			case 0:
				return zzqaOrderMsg{N: 5}
			case 1:
				return zzqaOrderMsg{N: 1}
			default:
				return zzqaOrderMsg{N: 9}
			}
		},
		Steps:  3,
		Models: []spectest.Refinement[zzqaOrderState, zzqaOrderMsg, int]{zzqaOrderModel(sortModelInPlace)},
	}
}

// The control: the same model sorting a copy leaves its own state alone, so the
// machine passes its refinement rule.
func TestAModelCheckThatSortsACopyDoesNotChangeLaterSteps(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaOrderMachine(false).Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("the control machine must pass its refinement rule, got:\n%s", msg)
	}
}

// A model's Agree that sorts the model state it is given is a write into the
// checker's own model. It must not change the model's next Step, which would
// make a correct machine fail a rule that is true of it.
func TestAModelCheckThatSortsItsStateDoesNotChangeLaterSteps(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaOrderMachine(true).Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("a model check that sorts the model state it is given changed the model's later steps, so a correct machine failed %s:\n%s",
			zzqaOrderRefines.ID, msg)
	}
}
