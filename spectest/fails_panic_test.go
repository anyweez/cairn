package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spectest"
)

// A panic anywhere in a check is a check that died, and Fails must report it
// as a failure rather than as a check that found nothing. A spec test written
// as
//
//	if msg, failed := spectest.Fails(t, m.Check); failed { t.Fatalf("...") }
//
// otherwise passes on a machine no run ever finished checking, which is the
// one outcome the checker must not produce.
//
// The generator is the fixture here because it is user code with no rule to
// attribute a panic to: a panic raised through rapid's harness ("[rapid]
// panic after N tests") is a failure a caller cannot recover from, even
// though rapid does not fail the *testing.T under Fails.
func TestFailsReportsACheckThatPanicked(t *testing.T) {
	m := counter(unreachable())
	m.AllowVacuous = []string{unreachable().ID}
	m.Messages = func(t *rapid.T, n int) int {
		// The first message moves the counter, so the panic lands partway
		// through a run rather than on the first draw.
		if n > 0 {
			panic("spectest: the generator cannot draw a message for this state")
		}
		return 1
	}

	msg, failed := spectest.Fails(t, m.Check)
	if !failed {
		t.Fatalf("a check that panicked must be reported as a failure, got failed=%v with: %s", failed, msg)
	}
	if !strings.Contains(msg, "the check did not finish") || !strings.Contains(msg, "panic") {
		t.Fatalf("the failure must say the check died and carry rapid's report, got: %s", msg)
	}
}
