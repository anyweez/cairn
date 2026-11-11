package spectest_test

// A spec check is handed the state the run is on. The state is a value, but
// its slice, map and pointer fields are shared with the run, so a check that
// sorts or otherwise rewrites one of them changes what the next message is
// decided from. That is not a rule the machine violates: the check itself
// returned Pass, and the ledger (which never runs the check) replays the
// stored events to exactly the state the run would have reached without it.
// The checker must not let a check change the run, and if it does report it,
// it has to name the rule whose check changed it rather than D5.

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

type zzqaRanked struct{ Items []int }

type zzqaRankMsg int

func zzqaRankRule() spec.Rule {
	return spec.Rule{ID: "ZR1", Kind: spec.Never, Status: spec.Established,
		Statement: "The ranked items are out of order."}
}

// zzqaRankMachine's invariant sorts what it was handed before checking it. The
// two machines differ only in whether the sort is done on the given slice or
// on a copy, so the control shows the machine and its events are fine.
func zzqaRankMachine(sortGiven bool) spectest.Machine[zzqaRanked, zzqaRankMsg, int, struct{}, struct{}] {
	return spectest.Machine[zzqaRanked, zzqaRankMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[zzqaRanked, zzqaRankMsg, int, struct{}, struct{}]{
			Initial: func() zzqaRanked { return zzqaRanked{Items: []int{2, 1}} },
			Decide: func(zzqaRanked, zzqaRankMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](0, nil)
			},
			Evolve: func(s zzqaRanked, _ struct{}) zzqaRanked { return s },
		},
		Messages: func(*rapid.T, zzqaRanked) zzqaRankMsg { return 0 },
		Steps:    2,
		Invariants: []spec.Invariant[zzqaRanked]{{Rule: zzqaRankRule(), Check: func(s zzqaRanked) spec.Verdict {
			items := s.Items
			if !sortGiven {
				items = slices.Clone(items)
			}
			slices.Sort(items)
			return spec.Check(slices.IsSorted(items), "items are %v", items)
		}}},
	}
}

// The control: a check that sorts a copy leaves the run alone, so the check
// finishes with no rule violated.
func TestACheckThatSortsACopyLeavesTheRunAlone(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaRankMachine(false).Check)
	if err != nil || failed {
		t.Fatalf("a check that only sorts a copy failed the run: err=%v failed=%v\n%s", err, failed, msg)
	}
}

// The defect: sorting the state the check was handed changes the state the
// run ends on, and D5 compares that changed state with the fold of the events
// the ledger stores. The machine's own rule passed, so the only acceptable
// outcomes are no violation or one attributed to ZR1.
func TestACheckThatSortsTheStateItIsHandedDoesNotChangeTheRun(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaRankMachine(true).Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed && !strings.Contains(msg, "ZR1 violated") {
		t.Fatalf("a check that returned Pass was reported as a violation of a rule the machine does not violate:\n%s", msg)
	}
}
