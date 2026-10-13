package spectest

import (
	"flag"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// Fails runs check against a recorder instead of the real test and returns the
// failure it reported, if any. It is how a spec proves it has teeth: swap in a
// mutant Decider, and assert the check fails.
//
//	m := machine()
//	m.Decider.Decide = mutant
//	if _, failed := spectest.Fails(t, m.Check); !failed { t.Fatal("mutant survived") }
func Fails(t testing.TB, check func(rapid.TB)) (message string, failed bool) {
	t.Helper()
	// A mutant's failure is the expected outcome, not one to reproduce later:
	// a saved fail file would be replayed against the next mutant of the same
	// test name instead of letting it search.
	if f := flag.Lookup("rapid.nofailfile"); f != nil {
		previous := f.Value.String()
		_ = f.Value.Set("true")
		defer func() { _ = f.Value.Set(previous) }()
	}
	r := &recorder{TB: t}
	func() {
		defer func() {
			if p := recover(); p != nil && p != errStop {
				panic(p)
			}
		}()
		check(r)
	}()
	return r.message.String(), r.failed
}

var errStop = &struct{ string }{"spectest: recorded failure"}

type recorder struct {
	testing.TB
	failed  bool
	message strings.Builder
}

func (r *recorder) Log(args ...any)                 {}
func (r *recorder) Logf(format string, args ...any) {}
func (r *recorder) Error(args ...any)               { r.Fail(); fmt.Fprintln(&r.message, args...) }
func (r *recorder) Errorf(format string, args ...any) {
	r.Fail()
	fmt.Fprintf(&r.message, format+"\n", args...)
}
func (r *recorder) Fatal(args ...any)                 { r.Error(args...); r.FailNow() }
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...); r.FailNow() }
func (r *recorder) Fail()                             { r.failed = true }
func (r *recorder) FailNow()                          { r.failed = true; panic(errStop) }
func (r *recorder) Failed() bool                      { return r.failed }
