package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// D2's statement, in spectest and in README.md's rule table, is "Decide or
// Evolve panics". The checker also holds Initial to it: a panicking Initial is
// reported as a D2 violation, and the rule line printed with that failure is
// the statement that does not mention Initial. A reader of the failure is told
// the machine broke a rule about two functions it did not panic in.
func TestTotalRuleStatementCoversAnInitialPanic(t *testing.T) {
	m := spectest.Machine[int, int, int, int, struct{}]{
		Decider: cairn.Decider[int, int, int, int, struct{}]{
			Initial: func() int { panic("the machine has no starting state") },
			Decide: func(n, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](n+msg, []int{msg})
			},
			Evolve: func(n, ev int) int { return n + ev },
		},
		Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(0, 1).Draw(t, "msg") },
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if !failed || !strings.Contains(msg, "D2 violated") {
		t.Fatalf("an Initial panic is a D2 violation, got failed=%v:\n%s", failed, msg)
	}
	for _, line := range strings.Split(msg, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "rule:") {
			continue
		}
		if !strings.Contains(line, "Initial") {
			t.Fatalf("the failure attributes an Initial panic to a rule whose statement does not cover Initial: %q", strings.TrimSpace(line))
		}
		return
	}
	t.Fatalf("the failure names no rule statement:\n%s", msg)
}
