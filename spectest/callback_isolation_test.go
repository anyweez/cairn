package spectest_test

// The runner hands more than an invariant and a law the state the run owns:
// the Messages generator, a Change's check and a refinement model's Step,
// SameResult and Agree see it too. Each of them holds slices, maps and
// pointers, so a callback that sorts one of them would otherwise change what
// later messages are decided from and what D5 folds. The invariant and law
// cases are zzqa_check_mutating_state_does_not_change_the_run_test.go and
// zzqa_law_mutating_events_does_not_change_the_run_test.go; this file covers
// the remaining hand-offs.

import (
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

type isolatedState struct{ Items []int }
type isolatedMsg struct{}

func isolatedMachine() spectest.Machine[isolatedState, isolatedMsg, int, struct{}, struct{}] {
	return spectest.Machine[isolatedState, isolatedMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[isolatedState, isolatedMsg, int, struct{}, struct{}]{
			Initial: func() isolatedState { return isolatedState{Items: []int{2, 1}} },
			Decide: func(s isolatedState, _ isolatedMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](0, nil)
			},
			Evolve: func(s isolatedState, _ struct{}) isolatedState { return s },
		},
		Messages: func(*rapid.T, isolatedState) isolatedMsg { return isolatedMsg{} },
		Steps:    2,
	}
}

// assertUnchanged runs the machine and requires it to finish without a
// violation. The callback under test writes through the state it is handed but
// otherwise passes, so a rule firing means the checker kept the write.
func assertUnchanged(t *testing.T, m spectest.Machine[isolatedState, isolatedMsg, int, struct{}, struct{}]) {
	t.Helper()
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed {
		t.Fatalf("the checker's callback write was reported as a violation of a rule the machine does not violate:\n%s", msg)
	}
}

func TestMessagesThatSortsTheStateItIsHandedDoesNotChangeTheRun(t *testing.T) {
	m := isolatedMachine()
	m.Messages = func(_ *rapid.T, s isolatedState) isolatedMsg {
		slices.Sort(s.Items)
		return isolatedMsg{}
	}
	assertUnchanged(t, m)
}

func TestAChangeThatSortsTheStateItIsHandedDoesNotChangeTheRun(t *testing.T) {
	rule := spec.Rule{ID: "ZR1", Kind: spec.Never, Status: spec.Established,
		Statement: "The items are out of order."}
	m := isolatedMachine()
	m.Changes = []spec.Change[isolatedState, isolatedMsg]{{Rule: rule,
		Check: func(before isolatedState, _ isolatedMsg, _ isolatedState) spec.Verdict {
			slices.Sort(before.Items)
			return spec.Check(slices.IsSorted(before.Items), "items are %v", before.Items)
		}}}
	assertUnchanged(t, m)
}

func TestAModelThatSortsTheStateItIsHandedDoesNotChangeTheRun(t *testing.T) {
	rule := spec.Rule{ID: "ZR1", Kind: spec.Refines, Status: spec.Established,
		Statement: "The implementation disagrees with the model."}
	m := isolatedMachine()
	m.Models = []spectest.Refinement[isolatedState, isolatedMsg, int]{spectest.Refines(
		spectest.Model[struct{}, isolatedState, isolatedMsg, int]{
			Rule:    rule,
			Initial: func() struct{} { return struct{}{} },
			Step: func(ms struct{}, _ isolatedMsg) (struct{}, int, *cairn.Refusal) {
				return ms, 0, nil
			},
			Agree: func(impl isolatedState, _ struct{}) spec.Verdict {
				slices.Sort(impl.Items)
				return spec.Check(slices.IsSorted(impl.Items), "items are %v", impl.Items)
			},
		})}
	assertUnchanged(t, m)
}

// A model's Step receives the model's own state. Writing through it must not
// stick when the step refuses, since a refusal leaves the state where it was.
func TestAModelStepThatSortsTheStateItIsHandedDoesNotChangeLaterSteps(t *testing.T) {
	rule := spec.Rule{ID: "ZR2", Kind: spec.Refines, Status: spec.Established,
		Statement: "The implementation disagrees with the model."}
	m := spectest.Machine[isolatedState, isolatedMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[isolatedState, isolatedMsg, int, struct{}, struct{}]{
			Initial: func() isolatedState { return isolatedState{Items: []int{2, 1}} },
			Decide: func(isolatedState, isolatedMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Refuse[int, struct{}, struct{}]("no", "refused")
			},
			Evolve: func(s isolatedState, _ struct{}) isolatedState { return s },
		},
		Messages: func(*rapid.T, isolatedState) isolatedMsg { return isolatedMsg{} },
		Steps:    1,
		Models: []spectest.Refinement[isolatedState, isolatedMsg, int]{spectest.Refines(
			spectest.Model[isolatedState, isolatedState, isolatedMsg, int]{
				Rule:    rule,
				Initial: func() isolatedState { return isolatedState{Items: []int{2, 1}} },
				Step: func(ms isolatedState, _ isolatedMsg) (isolatedState, int, *cairn.Refusal) {
					// Sort to inspect the order, then refuse. A refusal does not
					// advance the model, so a write kept here is a write into the
					// state the comparison and the next step read.
					slices.Sort(ms.Items)
					return ms, 0, &cairn.Refusal{Code: "no"}
				},
				Agree: func(impl isolatedState, model isolatedState) spec.Verdict {
					return spec.Check(slices.Equal(impl.Items, model.Items), "machine %v, model %v", impl.Items, model.Items)
				},
			})},
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("a model Step that sorted the state it was given changed the model's state, so the machine failed %s:\n%s", rule.ID, msg)
	}
}

// SameResult receives the model's expected result, which a model may share with
// its next state. Comparing a []int as a multiset means sorting it, and that
// write must not reorder the model's state.
type isolatedCountMsg struct{ N int }
type isolatedCountEvent struct{ N int }

func TestAModelSameResultThatSortsTheResultItIsHandedDoesNotChangeLaterSteps(t *testing.T) {
	rule := spec.Rule{ID: "ZR3", Kind: spec.Refines, Status: spec.Established,
		Statement: "The implementation disagrees with the model."}
	m := spectest.Machine[isolatedState, isolatedCountMsg, []int, isolatedCountEvent, struct{}]{
		Decider: cairn.Decider[isolatedState, isolatedCountMsg, []int, isolatedCountEvent, struct{}]{
			Initial: func() isolatedState { return isolatedState{} },
			Decide: func(s isolatedState, msg isolatedCountMsg) cairn.Decision[[]int, isolatedCountEvent, struct{}] {
				next := append(slices.Clone(s.Items), msg.N)
				return cairn.Accept[[]int, isolatedCountEvent, struct{}](next, []isolatedCountEvent{{N: msg.N}})
			},
			Evolve: func(s isolatedState, e isolatedCountEvent) isolatedState {
				return isolatedState{Items: append(slices.Clone(s.Items), e.N)}
			},
		},
		Messages: func(_ *rapid.T, s isolatedState) isolatedCountMsg {
			switch len(s.Items) {
			case 0:
				return isolatedCountMsg{N: 2}
			default:
				return isolatedCountMsg{N: 1}
			}
		},
		Steps: 2,
		Models: []spectest.Refinement[isolatedState, isolatedCountMsg, []int]{spectest.Refines(
			spectest.Model[isolatedState, isolatedState, isolatedCountMsg, []int]{
				Rule:    rule,
				Initial: func() isolatedState { return isolatedState{} },
				Step: func(ms isolatedState, msg isolatedCountMsg) (isolatedState, []int, *cairn.Refusal) {
					next := isolatedState{Items: append(slices.Clone(ms.Items), msg.N)}
					return next, next.Items, nil
				},
				Agree: func(impl isolatedState, model isolatedState) spec.Verdict {
					return spec.Check(slices.Equal(impl.Items, model.Items), "machine %v, model %v", impl.Items, model.Items)
				},
				SameResult: func(impl, model []int) bool {
					slices.Sort(impl)
					slices.Sort(model)
					return slices.Equal(impl, model)
				},
			})},
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("a model SameResult that sorted the result it was given reordered the model's state, so the machine failed %s:\n%s", rule.ID, msg)
	}
}
