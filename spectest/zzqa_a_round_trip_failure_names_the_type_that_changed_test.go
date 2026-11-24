package spectest_test

import (
	"encoding/json"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// anyNumber is a state with one field whose dynamic type JSON does not keep:
// decoding a number into an `any` yields float64 whatever was written, so a
// state holding int(5) really does come back holding float64(5). D7 is right to
// fail it.
type anyNumber struct{ N any }

// The failure detail of D7 (and of D6, which formats the same way) prints both
// sides with %+v, and %+v of int(5) and of float64(5) are the same text. So the
// report reads
//
//	D7 violated: state {N:5} came back as {N:5} (<nil>)
//
// which tells a reader that the state came back as itself. The one shape where
// the detail matters most is the one it cannot describe: a JSON round trip that
// changed the dynamic type inside an `any`, which is how a Go value stored as
// JSON loses its type. The detail has to let a reader see what changed.
func TestARoundTripFailureNamesTheTypeThatChanged(t *testing.T) {
	// The premise: the round trip this rule performs really does change the
	// state, so a failure here is the rule working and only the report is at
	// fault.
	state := anyNumber{N: 5}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("the state cannot be snapshotted at all: %v", err)
	}
	var back anyNumber
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("the snapshot cannot be read back: %v", err)
	}
	if _, ok := back.N.(float64); !ok {
		t.Fatalf("premise: a number in an `any` field came back as %T, not float64", back.N)
	}

	m := spectest.Machine[anyNumber, int, int, int, struct{}]{
		Decider: cairn.Decider[anyNumber, int, int, int, struct{}]{
			Initial: func() anyNumber { return state },
			Decide: func(s anyNumber, _ int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](0, nil)
			},
			Evolve: func(s anyNumber, _ int) anyNumber { return s },
		},
		Messages:  func(t *rapid.T, _ anyNumber) int { return rapid.Int().Draw(t, "msg") },
		Snapshots: true,
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if !failed || !strings.Contains(msg, spectest.Snapshottable.ID) {
		t.Fatalf("a state whose snapshot changes it must fail %s, got failed=%v:\n%s",
			spectest.Snapshottable.ID, failed, msg)
	}
	if !strings.Contains(msg, "float64") {
		t.Fatalf("the %s detail does not name the type the round trip produced, so it reports %q as unchanged:\n%s",
			spectest.Snapshottable.ID, "state {N:5} came back as {N:5}", msg)
	}
}
