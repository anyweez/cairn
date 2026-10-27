package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// A panic in any of a model's own functions is a violation of the rule the
// model checks, and the report must name it. model_panic_test.go covers the
// Proposed case, where the rule must be reported without failing the check;
// this covers the Established case, where it must fail and say which rule.
func TestModelPanicNamesItsRule(t *testing.T) {
	rule := spec.Rule{ID: "M1", Kind: spec.Refines, Status: spec.Established,
		Statement: "The machine agrees with the model."}
	cases := []struct {
		name  string
		model spectest.Model[int, int, int, int]
	}{
		{"Initial", spectest.Model[int, int, int, int]{
			Rule:    rule,
			Initial: func() int { panic("model initial exploded") },
			Step:    func(total, msg int) (int, int, *cairn.Refusal) { return total + msg, total + msg, nil },
			Agree:   func(impl, model int) spec.Verdict { return spec.Check(impl == model, "%d != %d", impl, model) },
		}},
		{"Step", spectest.Model[int, int, int, int]{
			Rule:    rule,
			Initial: func() int { return 0 },
			Step:    func(total, msg int) (int, int, *cairn.Refusal) { panic("model step exploded") },
			Agree:   func(impl, model int) spec.Verdict { return spec.Check(impl == model, "%d != %d", impl, model) },
		}},
		{"Agree", spectest.Model[int, int, int, int]{
			Rule:    rule,
			Initial: func() int { return 0 },
			Step:    func(total, msg int) (int, int, *cairn.Refusal) { return total + msg, total + msg, nil },
			Agree:   func(impl, model int) spec.Verdict { panic("model agree exploded") },
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := spectest.Machine[int, int, int, int, struct{}]{
				Decider: cairn.Decider[int, int, int, int, struct{}]{
					Initial: func() int { return 0 },
					Decide: func(n, msg int) cairn.Decision[int, int, struct{}] {
						return cairn.Accept[int, int, struct{}](n+msg, []int{msg})
					},
					Evolve: func(n, ev int) int { return n + ev },
				},
				Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(1, 2).Draw(t, "msg") },
				Models:   []spectest.Refinement[int, int, int]{spectest.Refines(tc.model)},
			}
			msg, failed, err := spectest.Fails(t, m.Check)
			if err != nil || !failed || !strings.Contains(msg, rule.ID+" violated") {
				t.Fatalf("a panic in the model's %s must be reported as %s violated, got err=%v failed=%v:\n%s",
					tc.name, rule.ID, err, failed, msg)
			}
		})
	}
}
