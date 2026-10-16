package fsm_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// The fsm package says "the spec checks that no state change ever happens
// outside" the table. fsm.Law checks only the state before a message and the
// state after it, so a message whose events leave the table and return within
// the same message is invisible: the net change is a no-op, and Law answers NA.
//
// This machine does exactly that. "flip" moves a -> b, which the table allows.
// "detour" folds two events that move the state out to "c" and back to where
// it was; the net change is a no-op, so the law never sees "c".
func TestLawChecksEveryStatusChangeNotJustTheNet(t *testing.T) {
	type ev string

	table := fsm.New(fsm.Edge[string, string]{From: "a", On: "flip", To: "b"})
	rule := spec.Rule{ID: "X1", Kind: spec.Never, Status: spec.Established,
		Statement: "A status changes outside the table."}

	decider := cairn.Decider[string, string, int, ev, struct{}]{
		Initial: func() string { return "a" },
		Decide: func(s string, m string) cairn.Decision[int, ev, struct{}] {
			switch m {
			case "flip":
				return cairn.Accept[int, ev, struct{}](0, []ev{ev("b")})
			case "detour":
				// Leave the table for "c", then come back to the state the
				// message was decided against.
				return cairn.Accept[int, ev, struct{}](0, []ev{ev("c"), ev(s)})
			}
			return cairn.Refuse[int, ev, struct{}]("unknown", "unknown message")
		},
		Evolve: func(_ string, e ev) string { return string(e) },
	}

	m := spectest.Machine[string, string, int, ev, struct{}]{
		Decider: decider,
		Messages: func(t *rapid.T, _ string) string {
			return rapid.SampledFrom([]string{"flip", "detour"}).Draw(t, "msg")
		},
		Changes: []spec.Change[string, string]{
			fsm.Law(rule, table, func(s string) string { return s }, func(m string) string { return m }),
		},
	}

	msg, failed := spectest.Fails(t, m.Check)
	if !failed || !strings.Contains(msg, rule.ID+" violated") {
		t.Fatalf("a status that left the table and returned was not caught: failed=%v msg=%s", failed, msg)
	}
}
