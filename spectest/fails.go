package spectest

import (
	"flag"
	"fmt"
	"strings"
	"sync"
	"testing"

	"pgregory.net/rapid"
)

// Fails runs check against a recorder instead of the real test and returns the
// first failure it reported, unshrunk, if any. It is how a spec proves it has teeth: swap in a
// mutant Decider, and assert the check fails.
//
//	m := machine()
//	m.Decider.Decide = mutant
//	if _, failed := spectest.Fails(t, m.Check); !failed { t.Fatal("mutant survived") }
//
// Fails changes process-wide rapid flags while it runs, so calls are
// serialized; a parallel test running an ordinary Check at the same time only
// loses its fail file and the shrinking of its counterexample.
func Fails(t testing.TB, check func(rapid.TB)) (message string, failed bool) {
	t.Helper()
	failsMu.Lock()
	defer failsMu.Unlock()
	// A mutant's failure is the expected outcome, not one to reproduce later:
	// a saved fail file would be replayed against the next mutant of the same
	// test name instead of letting it search.
	// Nor is it shrunk: callers ask whether and by which rule the check
	// fails, which the first failing run already answers, and shrinking it
	// costs seconds per mutant (tens of seconds under -race).
	for name, value := range map[string]string{"rapid.nofailfile": "true", "rapid.shrinktime": "0s"} {
		if f := flag.Lookup(name); f != nil {
			previous := f.Value.String()
			_ = f.Value.Set(value)
			defer func() { _ = f.Value.Set(previous) }()
		}
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

var failsMu sync.Mutex

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
