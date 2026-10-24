package spectest_test

// The checker keys its rule-health tallies by rule ID, so two different rules
// that happen to carry the same ID are merged into one row: their applied
// counts add up, and only the first declaration's statement is shown. That
// hides a rule that never applies, which the checker promises to report as
// vacuous, and it attributes a violation to the other rule's statement.
//
// The manifest tool already treats a rule ID declared twice as an error, so a
// machine with two different rules under one ID is refused rather than
// silently merged.

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// A minimal machine: a counter that adds the message it is given.
func idMergeMachine() spectest.Machine[int, int, int, int, struct{}] {
	return spectest.Machine[int, int, int, int, struct{}]{
		Decider: cairn.Decider[int, int, int, int, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(n, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](n+msg, []int{msg})
			},
			Evolve: func(n, ev int) int { return n + ev },
		},
		Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(0, 1).Draw(t, "msg") },
	}
}

func TestTwoRulesSharingAnIDAreNotMerged(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}

	// Two different rules, one ID. The first never applies; the second applies
	// on every state and holds. Merged by ID, the first looks exercised and
	// its vacuity goes unreported.
	neverApplies := spec.Rule{ID: "Z1", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is negative and even."}
	alwaysApplies := spec.Rule{ID: "Z1", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is checked under a second rule that reuses the ID."}

	m := idMergeMachine()
	m.Invariants = []spec.Invariant[int]{
		{Rule: neverApplies, Check: func(int) spec.Verdict { return spec.NA }},
		{Rule: alwaysApplies, Check: func(int) spec.Verdict { return spec.Pass }},
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check died instead of reporting the rule: %v", err)
	}
	if !failed {
		t.Fatalf("Z1 never applied as first declared, but the check passed: the two rules were merged by ID\n%s", msg)
	}
	if !strings.Contains(msg, "Z1") {
		t.Fatalf("the failure must name the rule ID, got %q", msg)
	}
}

// The same rule value used twice is one rule, not a duplicate: a rule can
// govern two fields of one machine, and both checks must count for it.
func TestTheSameRuleUsedTwiceIsOneRule(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	rule := spec.Rule{ID: "Z2", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is negative."}

	m := idMergeMachine()
	m.Invariants = []spec.Invariant[int]{
		{Rule: rule, Check: func(n int) spec.Verdict { return spec.Check(n >= 0, "counter is %d", n) }},
		{Rule: rule, Check: func(n int) spec.Verdict { return spec.Check(n <= 1000, "counter is %d", n) }},
	}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("one rule checked twice must pass: err=%v %s", err, msg)
	}
}
