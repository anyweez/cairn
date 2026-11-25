package cairn

import "testing"

// Step documents that a refused decision leaves the state as it was. A decider
// that returns a refusal alongside events is one the framework's own D3 rule
// rejects, but Step is the pure core's entry point and must not disagree with
// its documented contract when it is handed one: the events of a refused
// decision must not be folded.
func TestStepRefusesToFoldARefusalsEvents(t *testing.T) {
	d := Decider[int, int, int, int, int]{
		Initial: func() int { return 0 },
		Decide: func(s int, m int) Decision[int, int, int] {
			if m == 0 {
				return Decision[int, int, int]{Refusal: &Refusal{Code: "no"}}
			}
			return Accept[int, int, int](s+m, []int{m})
		},
		Evolve: func(s int, e int) int { return s + e },
	}

	// Control: an accepted decision still folds its events.
	if _, after := d.Step(0, 2); after != 2 {
		t.Fatalf("an accepted decision did not fold its event: state %d, want 2", after)
	}

	refused := Decision[int, int, int]{Refusal: &Refusal{Code: "no"}, Events: []int{2}}
	// Route it through Step by making Decide return it for a message.
	d.Decide = func(int, int) Decision[int, int, int] { return refused }
	decision, after := d.Step(0, 1)
	if !decision.Refused() {
		t.Fatalf("the decision was not refused: %+v", decision)
	}
	if after != 0 {
		t.Fatalf("Step folded a refused decision's events: state %d, want the state it was given (0)", after)
	}
}
