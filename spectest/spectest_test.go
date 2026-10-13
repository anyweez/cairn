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

func TestVacuousEstablishedRuleFails(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	msg, failed := spectest.Fails(t, counter(unreachable()).Check)
	if !failed || !strings.Contains(msg, "C1 is vacuous") {
		t.Fatalf("a rule that never applies must fail the check, got %q", msg)
	}
}

func TestAllowVacuous(t *testing.T) {
	m := counter(unreachable())
	m.AllowVacuous = []string{unreachable().ID}
	if msg, failed := spectest.Fails(t, m.Check); failed {
		t.Fatalf("an allowed vacuous rule must pass: %s", msg)
	}

	m.AllowVacuous = []string{"C9"}
	if msg, _ := spectest.Fails(t, m.Check); !strings.Contains(msg, "AllowVacuous names C9") {
		t.Fatalf("an unknown rule in AllowVacuous must be reported, got %q", msg)
	}
}

func TestVacuousProposedRulePasses(t *testing.T) {
	proposed := unreachable()
	proposed.Status = spec.Proposed
	if msg, failed := spectest.Fails(t, counter(proposed).Check); failed {
		t.Fatalf("a proposed rule only reports vacuity: %s", msg)
	}
}
