package spectest_test

// The checker identifies a rule by its whole value, not by its ID: two
// different rules that happen to carry the same ID are separate rules, so one
// that never applies is still reported vacuous (t3081, spectest/spectest.go's
// report). AllowVacuous is the one place left that matches by ID instead, so
// listing an ID exempts every rule that carries it — including one the user
// never meant, whose vacuity then goes unreported and silently weakens the
// check.
//
// A machine's rules are usually declared inside a function when they are
// fixtures, and the manifest tool only reads package-level declarations, so
// nothing refuses a duplicated ID here: the checker has to be right on its
// own.

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// allowVacuousCounter is a counter that adds each message, so its invariant
// holds on every state.
func allowVacuousCounter() spectest.Machine[int, int, int, int, struct{}] {
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

// Two different rules under one ID, only one of which the user is naming: the
// second is the rule the AllowVacuous entry is there for (say it is expected
// to apply only in a longer check). The first never applies at all.
func sharedIDMachine() spectest.Machine[int, int, int, int, struct{}] {
	never := spec.Rule{ID: "Z9", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is negative and even."}
	named := spec.Rule{ID: "Z9", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter is not negative, and this rule may skip a check."}
	m := allowVacuousCounter()
	m.Invariants = []spec.Invariant[int]{
		{Rule: never, Check: func(int) spec.Verdict { return spec.NA }},
		{Rule: named, Check: func(n int) spec.Verdict { return spec.Check(n >= 0, "counter is %d", n) }},
	}
	m.AllowVacuous = []string{"Z9"}
	return m
}

// The rule that never applies was not the one the AllowVacuous entry names, so
// it must still be reported vacuous, and the check must fail.
func TestAllowVacuousDoesNotExemptASecondRuleWithTheSameID(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	msg, failed, err := spectest.Fails(t, sharedIDMachine().Check)
	if err != nil {
		t.Fatalf("the check died instead of reporting the rule: %v", err)
	}
	if !failed {
		t.Fatalf("a rule that never applies was exempted by an AllowVacuous ID that names a different rule with the same ID\n%s", msg)
	}
	if !strings.Contains(msg, "Z9") {
		t.Fatalf("the failure must name the rule ID, got %q", msg)
	}
}

// The control: the same machine without the entry fails as vacuous, so a
// failure above is the exemption's scope rather than the vacuity gate being
// off or the never-applies rule being somehow exercised.
func TestWithoutTheAllowVacuousEntryTheSharedIDRuleIsReportedVacuous(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	m := sharedIDMachine()
	m.AllowVacuous = nil
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, "Z9 is vacuous") {
		t.Fatalf("control: err=%v failed=%v msg=%q", err, failed, msg)
	}
}

// A single declared rule named in AllowVacuous is still exempt, as documented:
// the exemption is not being taken away, only stopped from reaching a rule the
// user did not name.
func TestASingleRuleNamedInAllowVacuousIsStillExempt(t *testing.T) {
	if testing.Short() {
		t.Skip("vacuity is only enforced on a full-length check")
	}
	rule := spec.Rule{ID: "Z8", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter passes 1000."}
	m := allowVacuousCounter()
	m.Invariants = []spec.Invariant[int]{{Rule: rule, Check: func(int) spec.Verdict { return spec.NA }}}
	m.AllowVacuous = []string{"Z8"}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("an allowed vacuous rule must pass: err=%v %s", err, msg)
	}
}
