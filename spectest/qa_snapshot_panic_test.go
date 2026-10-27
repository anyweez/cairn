package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// boomState marshals itself for the ledger's snapshot round trip, and panics
// while doing so. A state that cannot survive the round trip is what D7
// (Snapshottable) exists to report, but checkSnapshot calls json.Marshal and
// the machine's Equal outside checked/guard, so the panic escapes to rapid as
// a harness panic that names no rule.
type boomState struct{ N int }

func (boomState) MarshalJSON() ([]byte, error) { panic("qa: the state cannot marshal itself") }

func TestAStateWhoseJSONEncodingPanicsNamesD7(t *testing.T) {
	m := spectest.Machine[boomState, int, int, int, struct{}]{
		Decider: cairn.Decider[boomState, int, int, int, struct{}]{
			Initial: func() boomState { return boomState{} },
			Decide: func(s boomState, _ int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](s.N+1, []int{1})
			},
			Evolve: func(s boomState, _ int) boomState { s.N++; return s },
		},
		Messages:  func(t *rapid.T, _ boomState) int { return rapid.Int().Draw(t, "n") },
		Snapshots: true,
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Snapshottable.ID) {
		t.Fatalf("a state whose JSON encoding panics must fail %s with its rule named, got err=%v failed=%v:\n%s",
			spectest.Snapshottable.ID, err, failed, msg)
	}
}
