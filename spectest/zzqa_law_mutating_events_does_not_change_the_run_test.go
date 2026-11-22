package spectest_test

// A `spec.Law` is handed the transition, including the decision's events. The
// events are not a copy: the slice is the one `Machine.run` accumulates for
// the replay, so a law that writes into it changes what D5 folds at the end of
// the run. Nothing in the ledger does that — `Aggregate.queueEntry` encodes
// the events `Decide` produced and then folds them — so a machine whose
// history replays correctly is failed by a rule about where the fold ended.
//
// Sorting the events is the ordinary way to write "the events are in order",
// and the check below sorts the slice it was handed rather than a clone. The
// failure must be no violation, or a violation naming the law's own rule; a
// D5 violation would mean the checker's write was reported as the machine's.

import (
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

func TestALawThatSortsTheEventsItIsHandedDoesNotChangeTheRun(t *testing.T) {
	// A fixture rule, declared in the function so the manifest tool does not
	// read it as a rule of the library.
	ordered := spec.Rule{ID: "ZR1", Kind: spec.Never, Status: spec.Established,
		Statement: "a message carries its events out of order."}

	m := spectest.Machine[[]int, struct{}, int, int, struct{}]{
		Decider: cairn.Decider[[]int, struct{}, int, int, struct{}]{
			Initial: func() []int { return nil },
			Decide: func(_ []int, _ struct{}) cairn.Decision[int, int, struct{}] {
				// Descending, so sorting the events the law is handed
				// changes the order the replay folds.
				return cairn.Accept[int, int, struct{}](0, []int{2, 1})
			},
			Evolve: func(s []int, e int) []int { return append(slices.Clone(s), e) },
		},
		Messages: func(t *rapid.T, _ []int) struct{} { return struct{}{} },
		Laws: []spec.Law[[]int, struct{}, int, int, struct{}]{{Rule: ordered,
			Check: func(tr spec.Transition[[]int, struct{}, int, int, struct{}]) spec.Verdict {
				slices.Sort(tr.Decision.Events)
				return spec.Check(true, "")
			}}},
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed && !strings.Contains(msg, "ZR1 violated") {
		t.Fatalf("a law that sorted the events it was handed was reported as a violation of a rule the machine does not violate: %s", msg)
	}
}
