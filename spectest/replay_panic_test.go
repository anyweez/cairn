package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

type replayPanicState struct{ N int }
type replayPanicMsg struct{ Add int }
type replayPanicEvent struct{ Add int }

// A Decider whose Initial panics on the call the checker makes to replay the
// run's events is not attributed to the rule it violates. The checker judges
// Total (D2) for Initial, and the replay fold is the call D5 (Replayable)
// makes, but the failure is rapid's harness panic and names no rule.
func TestReplayFoldPanicNamesARule(t *testing.T) {
	calls := 0
	m := spectest.Machine[replayPanicState, replayPanicMsg, int, replayPanicEvent, struct{}]{
		Decider: cairn.Decider[replayPanicState, replayPanicMsg, int, replayPanicEvent, struct{}]{
			Initial: func() replayPanicState {
				calls++
				if calls%2 == 0 {
					panic("Initial panicked on the replay call")
				}
				return replayPanicState{}
			},
			Decide: func(s replayPanicState, msg replayPanicMsg) cairn.Decision[int, replayPanicEvent, struct{}] {
				return cairn.Accept[int, replayPanicEvent, struct{}](s.N+msg.Add, []replayPanicEvent{{Add: msg.Add}})
			},
			Evolve: func(s replayPanicState, e replayPanicEvent) replayPanicState { s.N += e.Add; return s },
		},
		Messages: func(t *rapid.T, s replayPanicState) replayPanicMsg {
			return replayPanicMsg{Add: rapid.IntRange(0, 1).Draw(t, "add")}
		},
		Invariants: []spec.Invariant[replayPanicState]{{Rule: spec.Rule{ID: "R1", Kind: spec.Never, Status: spec.Established, Statement: "The counter is negative."}, Check: func(s replayPanicState) spec.Verdict {
			return spec.Check(s.N >= 0, "n is %d", s.N)
		}}},
	}

	msg, failed := spectest.Fails(t, m.Check)
	if !failed {
		t.Fatal("the check must fail when Initial panics on the replay call")
	}
	if !strings.Contains(msg, "D2 violated") && !strings.Contains(msg, "D5 violated") {
		t.Fatalf("a panic in the replay fold must be attributed to the rule it violates, got:\n%s", msg)
	}
}
