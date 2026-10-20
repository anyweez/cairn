package spectest_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// A model is a rule check, like a law, a change or an invariant, and CheckReport
// turns a panic in one of those into a violation of that rule rather than a
// crash of the whole run (spectest.checked). A panic inside a model's Step or
// Agree instead escapes to rapid, which fails the whole Check and names no
// rule. That is wrong on its own, and for a Proposed model — which must only
// be reported, never fail the build — it is a direct contradiction.
func TestProposedModelPanicIsReportedNotFatal(t *testing.T) {
	rule := spec.Rule{ID: "M1", Kind: spec.Refines, Status: spec.Proposed,
		Statement: "The machine agrees with the model."}
	m := spectest.Machine[int, int, int, int, struct{}]{
		Decider: cairn.Decider[int, int, int, int, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(n, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](n+msg, []int{msg})
			},
			Evolve: func(n, ev int) int { return n + ev },
		},
		Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(1, 2).Draw(t, "msg") },
		Models: []spectest.Refinement[int, int, int]{spectest.Refines(spectest.Model[int, int, int, int]{
			Rule:    rule,
			Initial: func() int { return 0 },
			Step: func(total, msg int) (int, int, *cairn.Refusal) {
				return total + msg, total + msg, nil
			},
			Agree: func(impl, model int) spec.Verdict { panic("model agrees exploded") },
		})},
	}
	if msg, failed := spectest.Fails(t, m.Check); failed {
		t.Fatalf("a Proposed model that panics must be reported, not fail the check: %s", msg)
	}
}
