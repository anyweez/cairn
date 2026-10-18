package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// spike is the state: N is the level, and may never exceed 50.
type spike struct{ N int }

// raise is an event that pushes N to 100; lower brings it back down. A single
// message folds both, so the state N=100 is reached while folding its events,
// even though the state before and after the message are both at most 50.
type raise struct{}
type lower struct{ To int }

// A rule that applies to every state and fails on the state the events pass
// through. It is not vacuous: at a message boundary N is 0 or the value lower
// restores, so checkState sees it apply and hold.
func atMostFifty() spec.Rule {
	return spec.Rule{ID: "E1", Kind: spec.Never, Status: spec.Established,
		Statement: "N passes 50, even between the events of one message."}
}

func spiky() spectest.Machine[spike, int, int, any, struct{}] {
	return spectest.Machine[spike, int, int, any, struct{}]{
		Decider: cairn.Decider[spike, int, int, any, struct{}]{
			Initial: func() spike { return spike{} },
			Decide: func(s spike, msg int) cairn.Decision[int, any, struct{}] {
				if msg == 1 {
					// One message, two events: through N=100 and back to 0.
					return cairn.Accept[int, any, struct{}](s.N, []any{raise{}, lower{To: 0}})
				}
				return cairn.Accept[int, any, struct{}](s.N, nil)
			},
			Evolve: func(s spike, e any) spike {
				switch e := e.(type) {
				case raise:
					s.N = 100
				case lower:
					s.N = e.To
				}
				return s
			},
		},
		Messages: func(t *rapid.T, _ spike) int { return rapid.IntRange(0, 1).Draw(t, "msg") },
		Invariants: []spec.Invariant[spike]{{Rule: atMostFifty(), Check: func(s spike) spec.Verdict {
			return spec.Check(s.N <= 50, "N is %d", s.N)
		}}},
		// The raise/lower events net to the same state, so folding a run's
		// events from the initial state reproduces it: no other rule fires.
		Steps: 3,
	}
}

// A message's events are folded one at a time, and each of those states is
// reached: rebuilding the stream from its events passes through them. An
// invariant is documented as checked against "every state reached", so the
// intermediate N=100 must be caught. It is not: the checker only judges the
// state after all of a message's events.
func TestInvariantChecksEveryStateAMessagesEventsPassThrough(t *testing.T) {
	if msg, failed := spectest.Fails(t, spiky().Check); !failed {
		t.Fatalf("the state a message's events passed through was not checked: failed=%v msg=%s\n"+
			"E1 is violated by N=100, which folding this message's events reaches", failed, msg)
	}
}

// The same rule, with the offending state left as the state after the message:
// it is caught. This rules out E1 being vacuous or its check never running, and
// shows the hole is exactly the states between a message's events.
func TestInvariantCatchesTheOffendingStateWhenItIsFinal(t *testing.T) {
	m := spiky()
	m.Decider.Decide = func(s spike, msg int) cairn.Decision[int, any, struct{}] {
		if msg == 1 {
			return cairn.Accept[int, any, struct{}](s.N, []any{raise{}})
		}
		return cairn.Accept[int, any, struct{}](s.N, nil)
	}
	if msg, failed := spectest.Fails(t, m.Check); !failed || !strings.Contains(msg, "E1 violated") {
		t.Fatalf("a state N=100 after a message must be caught by E1: failed=%v msg=%s", failed, msg)
	}
}
