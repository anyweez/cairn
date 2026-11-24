package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// qaTwoSlicesMsg carries two empty slices of different element types. They are
// distinct, non-nil values, but every zero-length slice in Go points at the
// runtime's zerobase, so they have the same data pointer.
type qaTwoSlicesMsg struct {
	Names  []string
	Counts []int
}

// A message with two empty slices of different types is a message the ledger
// stores and gives Decide like any other. The checker copies the message for
// each Decide call; the copy must not confuse the two slices, which share a
// data pointer only because both are empty. A panic in the checker's own copy
// is not a machine that panics, and D2 must not be reported.
func TestQATwoEmptySlicesInAMessageDoNotPanicTheCopy(t *testing.T) {
	m := spectest.Machine[struct{}, qaTwoSlicesMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[struct{}, qaTwoSlicesMsg, int, struct{}, struct{}]{
			Initial: func() struct{} { return struct{}{} },
			Decide: func(_ struct{}, msg qaTwoSlicesMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](len(msg.Names)+len(msg.Counts), nil)
			},
			Evolve: func(s struct{}, _ struct{}) struct{} { return s },
		},
		Messages: func(t *rapid.T, _ struct{}) qaTwoSlicesMsg {
			_ = rapid.Bool().Draw(t, "unused")
			return qaTwoSlicesMsg{Names: []string{}, Counts: []int{}}
		},
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("a machine that never panics was failed by the checker:\n%s", msg)
	}
	if strings.Contains(msg, "D2") {
		t.Fatalf("the checker's own message copy was reported as a machine panic:\n%s", msg)
	}
}
