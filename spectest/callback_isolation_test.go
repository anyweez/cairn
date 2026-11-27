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
