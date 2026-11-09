package spectest_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// README.md: "Vacuity is only enforced on a full-length check (rapid's default
// 100 runs). Under -short or a lowered -rapid.checks it is only reported,
// since a rarely applied rule can miss a short check by chance."
//
// A generator that rejects most draws produces many invalid rapid attempts.
// Machine.run counts every attempt in report.runs, including the ones rapid
// rejects through t.Skip, so a short check (rapid's 20 valid runs under
// -short) can still reach the 100 that gateVacuity uses as its "full-length"
// threshold. An established rule that simply is not reached by the 20 valid
// runs then fails a short check instead of only being reported.
func TestQAVacuityIsReportedNotEnforcedOnShortCheck(t *testing.T) {
	if !testing.Short() {
		t.Skip("the defect is that -short does not lower the vacuity gate; run with -short")
	}

	rule := spec.Rule{ID: "Z1", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is never negative."}

	// Reject the first 95 draws, then keep drawing valid messages. Under
	// -short rapid stops at 20 valid runs, but the recorder has already seen
	// 95 invalid attempts plus those 20 runs, more than vacuityRuns.
	draws := 0
	m := spectest.Machine[int, int, int, int, struct{}]{
		Decider: cairn.Decider[int, int, int, int, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(n, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](n+msg, []int{msg})
			},
			Evolve: func(n, ev int) int { return n + ev },
		},
		Messages: func(t *rapid.T, _ int) int {
			draws++
			if draws <= 95 {
				t.Skip("rejected draw")
			}
			return rapid.IntRange(0, 1).Draw(t, "msg")
		},
		Invariants: []spec.Invariant[int]{{Rule: rule, Check: func(n int) spec.Verdict {
			if n <= 1000 {
				return spec.NA
			}
			return spec.Check(n >= 0, "counter is %d", n)
		}}},
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the short check did not finish: %v", err)
	}
	if failed {
		t.Fatalf("a short check must only report vacuity, not fail on it:\n%s", msg)
	}
}
