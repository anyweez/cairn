package spectest_test

import (
	"math"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

type nanKeyState struct{ Counts map[float64]int }
type nanKeyMsg struct{}
type nanKeyEvent struct{ N int }

func nanKeyMachine() spectest.Machine[nanKeyState, nanKeyMsg, int, nanKeyEvent, struct{}] {
	return spectest.Machine[nanKeyState, nanKeyMsg, int, nanKeyEvent, struct{}]{
		Decider: cairn.Decider[nanKeyState, nanKeyMsg, int, nanKeyEvent, struct{}]{
			Initial: func() nanKeyState { return nanKeyState{Counts: map[float64]int{}} },
			Decide: func(s nanKeyState, m nanKeyMsg) cairn.Decision[int, nanKeyEvent, struct{}] {
				// The event carries a plain integer, so nothing the decision
				// holds is a NaN: the NaN is the key Evolve derives from it.
				return cairn.Accept[int, nanKeyEvent, struct{}](len(s.Counts), []nanKeyEvent{{N: 1}})
			},
			Evolve: func(s nanKeyState, e nanKeyEvent) nanKeyState {
				key := float64(e.N)
				if e.N == 1 {
					key = math.NaN()
				}
				next := make(map[float64]int, len(s.Counts)+1)
				for k, v := range s.Counts {
					next[k] = v
				}
				next[key]++
				return nanKeyState{Counts: next}
			},
		},
		Messages: func(*rapid.T, nanKeyState) nanKeyMsg { return nanKeyMsg{} },
		Invariants: []spec.Invariant[nanKeyState]{{Rule: spec.Rule{ID: "N1", Kind: spec.Never, Status: spec.Established,
			Statement: "The counter is negative."}, Check: func(s nanKeyState) spec.Verdict {
			for _, v := range s.Counts {
				if v < 0 {
					return spec.Fail("count %d", v)
				}
			}
			return spec.Pass
		}}},
	}
}

// A state whose fold reached a map keyed by NaN replays to exactly the state it
// reached: Fold builds the same map from the same events, and every value in it
// is the same. D5 must not report a divergence, and if it does the two values
// must not print identically.
func TestANaNMapKeyReplaysAsTheStateItReached(t *testing.T) {
	msg, failed, err := spectest.Fails(t, nanKeyMachine().Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed {
		t.Fatalf("a state keyed by NaN replays to itself, so D5 must pass; got:\n%s", msg)
	}
}

// The control: the same machine with an ordinary key does replay, so a failure
// above is the NaN key and not the machine.
func TestAnOrdinaryMapKeyReplaysAsTheStateItReached(t *testing.T) {
	m := nanKeyMachine()
	m.Decider.Evolve = func(s nanKeyState, e nanKeyEvent) nanKeyState {
		next := make(map[float64]int, len(s.Counts)+1)
		for k, v := range s.Counts {
			next[k] = v
		}
		next[float64(e.N)]++
		return nanKeyState{Counts: next}
	}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("the control machine must replay to itself: err=%v %s", err, msg)
	}
}
