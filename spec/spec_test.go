package spec_test

import (
	"testing"

	"github.com/anyweez/cairn/spec"
)

// A rule check that fails with an empty detail must still fail the check: the
// checker tells "the rule applied and failed" from "the rule applied and held"
// (and from "the rule did not apply") and must not depend on the failure text
// being non-empty.
func TestFailingVerdictWithEmptyDetailStillFails(t *testing.T) {
	for name, v := range map[string]spec.Verdict{
		"Check(false, \"\")": spec.Check(false, ""),
		"Fail(\"\")":         spec.Fail(""),
		"Fail(\"%s\", \"\")": spec.Fail("%s", ""),
	} {
		if _, failed := v.Failed(); !failed {
			t.Errorf("%s reported no failure, so a violated rule would pass silently", name)
		}
	}
}
