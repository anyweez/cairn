package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// Isolated (D4) says Decide must not modify the state it was given. The check
// calls Decide twice on the same state — once for the run, once to judge
// Deterministic (D1) — but fingerprints the state only after the first call, so
// a mutation made by the second call is never seen. A decider that writes into
// a slice's spare capacity on every call after the first therefore passes D4:
// the state's length and elements are unchanged, so Replayable's DeepEqual does
// not see it either.
func TestDecideIsolationIsCheckedAfterTheSecondCall(t *testing.T) {
	type state struct{ Moves []int }
	seen := map[*int]int{}

	m := spectest.Machine[state, int, int, int, struct{}]{
		Decider: cairn.Decider[state, int, int, int, struct{}]{
			Initial: func() state { return state{Moves: make([]int, 1, 2)} },
			Decide: func(s state, msg int) cairn.Decision[int, int, struct{}] {
				seen[&s.Moves[0]]++
				if seen[&s.Moves[0]] == 2 {
					// The checker's second call writes the spare capacity the
					// state shares with its successor; the state's own length
					// does not change.
					s.Moves = append(s.Moves, 42)
				}
				return cairn.Accept[int, int, struct{}](msg, []int{msg})
			},
			Evolve: func(s state, e int) state { return s },
		},
		Messages: func(t *rapid.T, _ state) int { return 0 },
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if !failed || !strings.Contains(msg, "D4 violated") {
		t.Fatalf("a Decide that writes into the given state's spare capacity on the checker's second call must violate D4; got failed=%v err=%v message=%q",
			failed, err, msg)
	}
}
