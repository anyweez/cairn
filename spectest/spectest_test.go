package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// unreachable can only apply above 1000, and the counter below never gets
// past 40. It is a fixture, not one of cairn's rules, so it is built in a
// function: the spec manifest lists package-level rules only.
func unreachable() spec.Rule {
	return spec.Rule{ID: "C1", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter passes 1000 and goes negative."}
}

// counter adds every message to its state.
func counter(rule spec.Rule) spectest.Machine[int, int, int, int, struct{}] {
	return spectest.Machine[int, int, int, int, struct{}]{
		Decider: cairn.Decider[int, int, int, int, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(n, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](n+msg, []int{msg})
			},
			Evolve: func(n, ev int) int { return n + ev },
		},
		Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(0, 1).Draw(t, "msg") },
		Invariants: []spec.Invariant[int]{{Rule: rule, Check: func(n int) spec.Verdict {
			if n <= 1000 {
				return spec.NA
			}
			return spec.Check(n >= 0, "counter is %d", n)
		}}},
	}
}

func TestEmptyFailureDetailFailsTheCheck(t *testing.T) {
	rule := spec.Rule{ID: "C2", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is checked with an empty failure detail."}
	m := counter(rule)
	m.Invariants = []spec.Invariant[int]{{Rule: rule, Check: func(int) spec.Verdict {
		return spec.Check(false, "")
	}}}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || !failed || !strings.Contains(msg, "C2 violated") {
		t.Fatalf("an empty failure detail must fail the check, got err=%v %q", err, msg)
	}
}

func TestVacuousEstablishedRuleFails(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	msg, failed, err := spectest.Fails(t, counter(unreachable()).Check)
	if err != nil || !failed || !strings.Contains(msg, "C1 is vacuous") {
		t.Fatalf("a rule that never applies must fail the check, got err=%v %q", err, msg)
	}
}

// The vacuity gate counts the runs rapid completed, not the draws a generator
// rejected. A full-length check still performs its 100 valid runs when some
// draws are rejected, so an established rule none of them reached must still
// fail it: the -short exception must not become a hole at full length.
func TestVacuousEstablishedRuleStillFailsWhenTheGeneratorRejectsDraws(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	m := counter(unreachable())
	draws := 0
	m.Messages = func(t *rapid.T, _ int) int {
		draws++
		if draws <= 5 {
			t.Skip("not a valid draw")
		}
		return rapid.IntRange(0, 1).Draw(t, "msg")
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, "C1 is vacuous") {
		t.Fatalf("a full-length check must still fail a rule no valid run reached, got err=%v %q", err, msg)
	}
}

func TestAllowVacuous(t *testing.T) {
	m := counter(unreachable())
	m.AllowVacuous = []string{unreachable().ID}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("an allowed vacuous rule must pass: err=%v %s", err, msg)
	}

	m.AllowVacuous = []string{"C9"}
	if msg, _, err := spectest.Fails(t, m.Check); err != nil || !strings.Contains(msg, "AllowVacuous names C9") {
		t.Fatalf("an unknown rule in AllowVacuous must be reported, got %q", msg)
	}
}

// rapid's T.Skip rejects a draw that is not a valid input, and a generator
// that rejects most draws makes rapid report that it generated too few valid
// tests: the harness never got its look at the rule, so the check has no
// verdict either way. That is a kill neither — counting one would let a
// mutant test pass without the rule ever being exercised — and it is not a
// survival either, so Fails reports it as inconclusive.
func TestFailsReportsTooFewValidRunsAsInconclusive(t *testing.T) {
	m := counter(unreachable())
	m.AllowVacuous = []string{unreachable().ID}
	m.Messages = func(t *rapid.T, _ int) int {
		if rapid.IntRange(0, 99).Draw(t, "valid") != 0 {
			t.Skip("not a valid draw")
		}
		return rapid.IntRange(0, 1).Draw(t, "msg")
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if failed {
		t.Fatalf("a rejected draw is not a rule violation: %s", msg)
	}
	if err == nil {
		t.Fatalf("a check that never generated enough valid runs has no verdict: %s", msg)
	}
}

// The harness message must not swallow a real violation: a generator that
// rejects most draws is exactly when a mutant test is most likely to pass for
// the wrong reason, so a rule that does fire must still be reported.
func TestFailsStillCatchesARuleViolationWhenTheGeneratorRejectsMostDraws(t *testing.T) {
	rule := spec.Rule{ID: "C3", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter moved off zero."}
	m := counter(rule)
	m.Invariants = []spec.Invariant[int]{{Rule: rule, Check: func(n int) spec.Verdict {
		return spec.Check(n == 0, "counter moved to %d", n)
	}}}
	m.Messages = func(t *rapid.T, _ int) int {
		if rapid.IntRange(0, 99).Draw(t, "valid") != 0 {
			t.Skip("not a valid draw")
		}
		return 1
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, "C3 violated") {
		t.Fatalf("a violated rule must fail even when the generator rejects most draws, got err=%v %q", err, msg)
	}
}

func TestVacuousProposedRulePasses(t *testing.T) {
	proposed := unreachable()
	proposed.Status = spec.Proposed
	if msg, failed, err := spectest.Fails(t, counter(proposed).Check); err != nil || failed {
		t.Fatalf("a proposed rule only reports vacuity: err=%v %s", err, msg)
	}
}
