package spectest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spec"
)

// Fails runs check against a recorder instead of the real test and reports how
// it finished. It is how a spec proves it has teeth: swap in a mutant Decider,
// and assert the check fails — and that the rule the mutant was aimed at is
// the one that fired.
//
//	m := machine()
//	m.Decider.Decide = mutant
//	msg, failed, err := spectest.Fails(t, m.Check)
//	if err != nil {
//		t.Fatalf("the check did not finish cleanly: %v", err)
//	}
//	if !failed || !strings.Contains(msg, "B1 violated") {
//		t.Fatal("mutant survived")
//	}
//
// Asserting on the rule's name as well as on failed is what keeps a mutant
// from passing because the wrong rule fired.
//
// Fails has three outcomes, not two:
//
//   - failed=true: a rule was violated, or the machine said something
//     spectest refuses outright (a law whose Kind is Refines, an established
//     rule that never applied). The message carries the rule's name. This is
//     the kill a mutant test wants.
//   - err != nil: the check did not produce a verdict. The Messages generator
//     — the one piece of user code no rule guards — panicked, or rapid's
//     harness gave up in a way Fails has no rule to attribute. Neither kill
//     nor survival, so a mutant test that hits this one fails loudly instead
//     of passing without a rule having caught anything.
//   - otherwise: the check finished, no rule fired; the mutant survived.
//
// A mutant's failure is the expected outcome, not one to reproduce later, so
// Fails does not let rapid see it: rapid would shrink it and save a fail file
// under the outer test's name, which the next run of the same name replays
// instead of drawing again, and both paths read the process-wide rapid flags
// a concurrent Check also reads. Instead the first violation is recorded and
// the check winds down without failing rapid. No process-wide rapid flag is
// changed, so a Fails may run beside a parallel Check.
//
// A check that dies still travels rapid's teardown on the way out — the death
// is attributed below — so rapid writes a fail file under the test's name
// before Fails returns. Fails deletes whatever this name holds: a replayed
// fail file is never what it wants, since a kill is the expected outcome and a
// death is reported to the caller.
//
// What Fails reports is decided from what the code did — a verdict a rule
// recorded, or spectest refusing the machine — never from the wording of
// rapid's reports: every report reaching the recorder that spectest has not
// itself marked is treated as a check that produced no verdict, whatever it
// says. So a rapid this file has not been met cannot make a surviving mutant
// pass again.
//
// The recorder is found through the context rapid derives from its TB (see
// recorder.Context), not by asserting the TB the check is handed, so a caller
// that nests m.Check inside its own rapid.Check still gets a kill. A TB that
// wraps the recorder without forwarding Context() is not followed: its
// violations come back as an error, which is safe but not the kill the mutant
// test is waiting for.
func Fails(t testing.TB, check func(rapid.TB)) (message string, failed bool, err error) {
	t.Helper()
	r := &recorder{TB: t}
	func() {
		defer func() {
			if p := recover(); p != nil && p != errStop {
				panic(p)
			}
		}()
		check(r)
	}()
	// rapid saves a fail file for anything it saw fail: a check that died, or a
	// violation reported through a TB Fails cannot follow. The next run of this
	// name would replay it before drawing again, which is a shrink of a check
	// Fails did not ask rapid to fail.
	dropFailFiles(t.Name())
	if r.died {
		return r.message.String(), false, errors.New("the check did not finish cleanly: " + r.death)
	}
	return r.message.String(), r.violated, nil
}

var errStop = &struct{ string }{"spectest: recorded failure"}

type recorder struct {
	testing.TB
	// failed is what rapid sees through TB.Failed: it is set by any error,
	// including rapid's own harness messages, so rapid's run loop behaves as it
	// would against a real *testing.T.
	failed bool
	// violated is set when a rule is violated and when spectest refuses the
	// machine outright, so Fails can tell a caught mutant from a check that
	// produced no verdict. See markViolation.
	violated bool
	// died is set when the check finished without a verdict: a report rapid's
	// harness makes that spectest has not recorded a violation for (see
	// Errorf). It stops the check the way a violation does, so no later run
	// redraws the same death.
	died    bool
	message strings.Builder
	// death is the detail of the first thing that made the check inconclusive.
	death string
}

// failsKey carries a Fails' recorder in the context rapid derives from its TB.
type failsKey struct{}

// Context is how a violation reaches the recorder from inside a check that
// nested m.Check in its own rapid.Check: rapid derives a *rapid.T's context
// from the TB it was given, so the recorder is above that T even though the
// machine is handed the T and not the recorder. A type assertion on the TB a
// check is handed misses it; the context does not.
func (r *recorder) Context() context.Context {
	return context.WithValue(context.Background(), failsKey{}, r)
}

// failsRecorder returns the recorder the check t runs under, or nil for an
// ordinary Check (or a TB that wraps the recorder without forwarding
// Context).
func failsRecorder(t rapid.TB) *recorder {
	c, ok := t.(interface{ Context() context.Context })
	if !ok {
		return nil
	}
	r, _ := c.Context().Value(failsKey{}).(*recorder)
	return r
}

// failFileGlob is where rapid keeps one test's fail files: testdata/rapid/
// <name>/, with the name reduced to letters, digits, '-' and '_' (rapid's
// kindaSafeFilename). If rapid changes that rule this finds nothing and the
// file stays where it was, no worse than not cleaning up at all.
func failFileGlob(testName string) string {
	var b strings.Builder
	for _, c := range testName {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return filepath.Join("testdata", "rapid", b.String(), "*.fail")
}

// dropFailFiles removes the fail files rapid has under a test's name. A file
// records a failure Fails deliberately did not let rapid fail — a check that
// died, or a violation reported through a TB Fails could not follow — and
// rapid replays it on the next run of the same test before drawing anything
// new.
func dropFailFiles(testName string) {
	pattern := failFileGlob(testName)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, match := range matches {
		if err := os.Remove(match); err != nil {
			return
		}
	}
	_ = os.Remove(filepath.Dir(pattern)) // only succeeds once the directory is empty
}

// recordViolation is how judge reports an established rule violation while
// Fails is running. Recording it instead of failing rapid keeps rapid out of
// its fail-file and shrinking paths, which read the process-wide flags a
// concurrent Check also reads. It returns whether this was the first violation.
func (r *recorder) recordViolation(rule spec.Rule, detail string) bool {
	if r.violated {
		return false
	}
	r.violated = true
	fmt.Fprintf(&r.message, "%s violated: %s\n  rule: %s\n", rule.ID, detail, rule.Statement)
	return true
}

// stopping reports that a violation has already been recorded or the check has
// died, so the rest of it proves nothing and can be skipped.
func (r *recorder) stopping() bool { return r.violated || r.died }

// markViolation records that the check failed for a reason other than a rule
// verdict counting: spectest reporting one directly (a Kind mismatch, a
// vacuous rule). Either way Fails returns it as a kill, unlike an unattributed
// report from rapid's harness, which is a check that produced no verdict.
func (r *recorder) markViolation() { r.violated = true }

// recordDeath records that the check ended without a verdict: rapid's
// harness gave up on it (a panic in the Messages generator — the one piece of
// user code no rule guards — a shortfall, a wording this checkout has not
// met). It is a check that died, not a rule the machine violated: a mutant
// whose broken state makes the Messages generator panic has not been caught
// by anything, and must fail loudly, not pass.
func (r *recorder) recordDeath(detail string) {
	if r.died {
		return
	}
	r.died = true
	r.Fail()
	fmt.Fprintf(&r.message, "the check did not finish cleanly: %s\n", detail)
	r.death = detail
}

func (r *recorder) Log(args ...any)                 {}
func (r *recorder) Logf(format string, args ...any) {}
func (r *recorder) Error(args ...any)               { r.Errorf("%s", fmt.Sprint(args...)) }
func (r *recorder) Errorf(format string, args ...any) {
	r.Fail()
	if r.violated {
		// spectest's own reporters mark the violation first (markViolation)
		// and then say it: a Kind mismatch, a vacuous rule.
		fmt.Fprintf(&r.message, format+"\n", args...)
		return
	}
	if r.died {
		return
	}
	// An error the recorder has not itself marked is rapid's harness speaking
	// once the runs are over. On a check that finished without dying, the only
	// such report in rapid v1.2.0 is the generation shortfall ("only generated
	// N valid tests"): a generator that rejected too many draws. Anything else
	// this channel carries is a wording Fails has no rule to attribute. A
	// shortfall is a check that never got its look at the rule — the mutant
	// test has no verdict either way — so it is recorded as a death, not as a
	// rule caught.
	r.recordDeath(fmt.Sprintf(format, args...))
}
func (r *recorder) Fatal(args ...any)                 { r.Error(args...); r.FailNow() }
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); r.FailNow() }
func (r *recorder) Fail()                             { r.failed = true }
func (r *recorder) FailNow()                          { r.failed = true; panic(errStop) }
func (r *recorder) Failed() bool                      { return r.failed }

// markViolation records, for Fails' recorder, a check that failed for a reason
// its rule verdicts do not show — spectest reporting one directly, or rapid
// giving up on the run. It is a no-op for a plain testing.TB.
func markViolation(t rapid.TB) {
	if r := failsRecorder(t); r != nil {
		r.markViolation()
	}
}
