package spectest

import (
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spec"
)

// Fails runs check against a recorder instead of the real test and returns the
// first failure a spectest rule reported, unshrunk, if any. It is how a spec
// proves it has teeth: swap in a mutant Decider, and assert the check fails.
//
//	m := machine()
//	m.Decider.Decide = mutant
//	if _, failed := spectest.Fails(t, m.Check); !failed { t.Fatal("mutant survived") }
//
// A generation shortfall is not a rule failure: a Messages generator that
// rejects most draws with t.Skip makes rapid report "only generated N valid
// tests", and counting that would let the mutant test pass without the rule
// ever being exercised. A check that died is a failure, though: a panic in
// user code with no rule to attribute it to (the Messages generator is the one
// left) reaches rapid, which reports it on that same channel, and a mutant
// test must not pass on a machine no run finished checking.
//
// A mutant's failure is the expected outcome, not one to reproduce later, so
// Fails does not let rapid see it: a saved fail file would be replayed against
// the next mutant of the same test name instead of letting it search, and
// shrinking costs seconds per mutant (tens of seconds under -race). Instead the
// first violation is recorded and the check winds down without failing rapid.
// No process-wide rapid flag is changed, so a Fails may run beside a parallel
// Check.
func Fails(t testing.TB, check func(rapid.TB)) (message string, failed bool) {
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
	return r.message.String(), r.violated
}

var errStop = &struct{ string }{"spectest: recorded failure"}

type recorder struct {
	testing.TB
	// failed is what rapid sees through TB.Failed: it is set by any error,
	// including rapid's own harness messages, so rapid's run loop behaves as it
	// would against a real *testing.T.
	failed bool
	// violated is set when a rule is violated and when rapid reports that the
	// check itself died, so Fails can tell either from the generation-shortfall
	// message a rejecting generator produces. See Errorf.
	violated bool
	message  strings.Builder
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

// stopping reports that a violation has already been recorded, so the rest of
// the check can be skipped.
func (r *recorder) stopping() bool { return r.violated }

// markViolation records that the check failed for a reason other than a rule
// verdict counting: spectest reported it directly (a Kind mismatch, a vacuous
// rule), or rapid reported that the check died. Either way Fails returns it as
// a failure, unlike the generation shortfall, which does not call this.
func (r *recorder) markViolation() { r.violated = true }

// checkDied reports whether format is rapid's own report that a check died
// rather than simply found nothing: a panic in the property, a failure it
// could not attribute to a rule, or a failure it could not reproduce. All are
// reported through testing.TB.Errorf, and the wording is what separates them
// from the generation shortfall, which is the one message on that channel that
// is not a failure. Anything else rapid starts reporting this way is treated
// as a death, so a new rapid message cannot silently make Fails pass again.
func checkDied(format string) bool {
	return strings.HasPrefix(format, "[rapid] ") &&
		!strings.HasPrefix(format, "[rapid] only generated ")
}

func (r *recorder) Log(args ...any)                 {}
func (r *recorder) Logf(format string, args ...any) {}
func (r *recorder) Error(args ...any)               { r.Fail(); fmt.Fprintln(&r.message, args...) }
func (r *recorder) Errorf(format string, args ...any) {
	r.Fail()
	if checkDied(format) {
		// rapid reports that the property panicked (or failed in a way it could
		// not attribute, or could not reproduce) through Errorf, the same
		// channel as the generation shortfall above. Nothing else records it
		// under Fails, so without this the check reads as one that found
		// nothing. Failing closed on rapid's messages keeps that true if rapid
		// adds another way to give up.
		r.markViolation()
		fmt.Fprint(&r.message, "the check did not finish: ")
	}
	fmt.Fprintf(&r.message, format+"\n", args...)
}
func (r *recorder) Fatal(args ...any)                 { r.Error(args...); r.FailNow() }
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); r.FailNow() }
func (r *recorder) Fail()                             { r.failed = true }

// markViolation is a no-op for a plain testing.TB: only Fails' recorder needs
// to record a check that failed for a reason its rule verdicts do not show —
// spectest reporting one directly, or rapid giving up on the run.
func markViolation(t rapid.TB) {
	if m, ok := t.(interface{ markViolation() }); ok {
		m.markViolation()
	}
}
func (r *recorder) FailNow()     { r.failed = true; panic(errStop) }
func (r *recorder) Failed() bool { return r.failed }
