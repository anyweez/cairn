package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spectest"
)

// A panic anywhere in a check is a check that died, and Fails must report it
// as neither a kill nor a survival. A mutant test written as
//
//	if _, failed, err := spectest.Fails(t, m.Check); !failed { ... }
//
// otherwise passes on a machine no run ever finished checking, which is the
// one outcome the checker must not produce: the mutant survived only in the
// sense that nothing ran to catch it.
//
// The generator is the fixture here because it is user code with no rule to
// attribute a panic to: Decide, Evolve and the laws turn a panic into a
// violation of the rule that owns them, but the Messages generator is the one
// left, so a mutant whose broken state makes the generator panic makes the
// whole check die.
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

	msg, failed, err := spectest.Fails(t, m.Check)
	if err == nil || failed {
		t.Fatalf("a check that panicked must be inconclusive, not caught: failed=%v err=%v with: %s", failed, err, msg)
	}
	if !strings.Contains(msg, "the check did not finish cleanly") {
		t.Fatalf("the inconclusive outcome must say the check did not finish, got: %s", msg)
	}
	if !strings.Contains(msg, "cannot draw a message for this state") || !strings.Contains(msg, "fails_panic_test.go") {
		t.Fatalf("the death must carry the panic itself and where it panicked, got: %s", msg)
	}
}

// An error Fails cannot attribute to a rule is a check that produced no
// verdict. The generation shortfall reaches the recorder through the same
// channel rapid reports everything else on, and any future report arrives the
// same way, so an unrecognized one is taken as inconclusive — never as a rule
// caught, which would make a surviving mutant pass again.
func TestFailsTreatsAnUnattributedErrorAsInconclusive(t *testing.T) {
	if _, failed, err := spectest.Fails(t, func(t rapid.TB) {
		t.Errorf("[rapid] report wording this checkout has never seen")
	}); err == nil || failed {
		t.Fatalf("a report Fails cannot attribute is neither a kill nor a survival, got failed=%v err=%v", failed, err)
	}
}
