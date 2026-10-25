package spectest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// A check that dies still travels rapid's teardown, which saves a fail file
// under the test's name. The next run of that name replays it instead of
// drawing again, so a mutant test that died once would keep replaying the
// shrink of a check no rule ever judged. Fails removes it before returning; a
// file left there is a failure waiting to be replayed against a machine that
// may have changed.
func TestFailsLeavesNoFailFileBehind(t *testing.T) {
	dir := filepath.Join("testdata", "rapid", "TestFailsLeavesNoFailFileBehind")
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	_ = os.RemoveAll(dir) // a fail file from an earlier run would be replayed

	m := counter(unreachable())
	m.AllowVacuous = []string{unreachable().ID}
	m.Messages = func(t *rapid.T, n int) int {
		if n > 0 {
			panic("spectest: dying on purpose")
		}
		return 1
	}
	if _, failed, err := spectest.Fails(t, m.Check); err == nil || failed {
		t.Fatalf("the check must die rather than be caught: failed=%v err=%v", failed, err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.fail"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 0 {
		t.Fatalf("Fails left a rapid fail file the next run would replay: %v", files)
	}

	// A file left by an earlier run is replayed before anything is drawn, so a
	// clean run has to clear it too: Fails never wants rapid to run a saved
	// case, whether or not the case still reproduces.
	stale := filepath.Join(dir, "TestFailsLeavesNoFailFileBehind-19700101000000-1.fail")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("not a fail file"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean := counter(unreachable())
	clean.AllowVacuous = []string{unreachable().ID}
	if _, failed, err := spectest.Fails(t, clean.Check); err != nil || failed {
		t.Fatalf("an allowed vacuous rule must pass: failed=%v err=%v", failed, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a stale fail file survived a clean Fails run: %v", err)
	}
}

// A caller may nest m.Check inside its own rapid.Check, so the machine is
// handed a *rapid.T rather than the recorder Fails created. A violation used
// to come back as a check that produced no verdict — the error a mutant test
// treats as a broken harness instead of the kill it is waiting for. The
// recorder travels in the context rapid derives from its TB, so the kill still
// arrives.
func TestFailsCatchesAViolationInsideANestedCheck(t *testing.T) {
	rule := spec.Rule{ID: "C5", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter has a value it can never hold."}
	m := counter(rule)
	m.Invariants = []spec.Invariant[int]{{Rule: rule, Check: func(int) spec.Verdict {
		return spec.Check(false, "nope")
	}}}

	msg, failed, err := spectest.Fails(t, func(tb rapid.TB) {
		rapid.Check(tb, func(rt *rapid.T) { m.Check(rt) })
	})
	if err != nil || !failed || !strings.Contains(msg, "C5 violated") {
		t.Fatalf("a violation inside a nested check must be a kill, got failed=%v err=%v with: %s", failed, err, msg)
	}
}

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

// recorder.Error and recorder.Fatal report through the same path as the
// formatted forms: an error spectest has not marked is a check that produced
// no verdict, not one that survived. Reporting a survival beside the error
// would hand a mutant test the wrong answer in the one direction that must
// never be wrong.
func TestFailsClassifiesPlainErrorAndFatal(t *testing.T) {
	for name, fail := range map[string]func(rapid.TB){
		"Error": func(tb rapid.TB) { tb.Error("boom") },
		"Fatal": func(tb rapid.TB) { tb.Fatal("boom") },
	} {
		if _, failed, err := spectest.Fails(t, fail); err == nil || failed {
			t.Errorf("%s must be inconclusive, got failed=%v err=%v", name, failed, err)
		}
	}
}
