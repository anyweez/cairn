package spectest_test

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// pointerKeyState keeps a set of entries keyed by identity: the key points at
// the value it names, and the state is only ever compared by what the keys
// reach. Initial mints the key it owns, which is what a stream rebuilt from
// its events does too, so replay must compare the two maps structurally rather
// than by the address inside each key.
type pointerKeyState struct{ Entries map[*int]string }

type pointerKeyMsg struct{ Add *int }

func initialPointerKeyState() pointerKeyState {
	key := 1
	return pointerKeyState{Entries: map[*int]string{&key: "initial"}}
}

func pointerKeyMachine() spectest.Machine[pointerKeyState, pointerKeyMsg, int, *int, struct{}] {
	return spectest.Machine[pointerKeyState, pointerKeyMsg, int, *int, struct{}]{
		Decider: cairn.Decider[pointerKeyState, pointerKeyMsg, int, *int, struct{}]{
			Initial: initialPointerKeyState,
			Decide: func(s pointerKeyState, msg pointerKeyMsg) cairn.Decision[int, *int, struct{}] {
				return cairn.Accept[int, *int, struct{}](0, []*int{msg.Add})
			},
			Evolve: func(s pointerKeyState, key *int) pointerKeyState {
				entries := make(map[*int]string, len(s.Entries)+1)
				for k, v := range s.Entries {
					entries[k] = v
				}
				entries[key] = "added"
				s.Entries = entries
				return s
			},
		},
		Messages: func(t *rapid.T, _ pointerKeyState) pointerKeyMsg {
			n := rapid.IntRange(2, 3).Draw(t, "entry")
			return pointerKeyMsg{Add: &n}
		},
		Steps: 3,
	}
}

// D5 (Replayable) folds the run's events from a fresh Initial. Every map key
// the initial state minted is a different pointer the second time, though the
// key still reaches the same value. A deterministic machine that never changes
// an entry must pass.
func TestReplayAcceptsAStateKeyedByPointerIdentity(t *testing.T) {
	if msg, failed, err := spectest.Fails(t, pointerKeyMachine().Check); err != nil || failed {
		t.Fatalf("a deterministic state keyed by pointer identity must replay: err=%v failed=%v\n%s", err, failed, msg)
	}
}
