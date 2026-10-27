package spectest_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// A proposed rule whose first counterexample fails with an empty detail must
// report that first counterexample. Whether a rule failed is a bit separate
// from its text, so an empty detail is not "no failure yet".
func TestProposedViolationKeepsAnEmptyDetail(t *testing.T) {
	rule := spec.Rule{ID: "C3", Kind: spec.Never, Status: spec.Proposed,
		Statement: "The counter is reported with its shortest trace."}
	m := counter(rule)
	m.Invariants = []spec.Invariant[int]{{Rule: rule, Check: func(n int) spec.Verdict {
		if n == 0 {
			return spec.Fail("")
		}
		return spec.Fail("counter is %d", n)
	}}}
	var violations []spectest.Violation
	if msg, failed, err := spectest.Fails(t, func(t rapid.TB) { violations = m.CheckReport(t) }); err != nil || failed {
		t.Fatalf("a proposed rule must not fail the check: err=%v %s", err, msg)
	}
	if len(violations) != 1 || violations[0].Rule.ID != rule.ID {
		t.Fatalf("want one violation of %s, got %+v", rule.ID, violations)
	}
	if violations[0].Detail != "" {
		t.Fatalf("want the first counterexample's empty detail, got %q", violations[0].Detail)
	}
}
