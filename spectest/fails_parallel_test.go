package spectest_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// captureTB records a failing check without failing the real test, so the only
// thing this file asserts is the race detector's verdict.
type captureTB struct{ failed atomic.Bool }

func (c *captureTB) Helper()               {}
func (c *captureTB) Name() string          { return "capture" }
func (c *captureTB) Logf(string, ...any)   {}
func (c *captureTB) Log(...any)            {}
func (c *captureTB) Skipf(string, ...any)  {}
func (c *captureTB) Skip(...any)           {}
func (c *captureTB) SkipNow()              {}
func (c *captureTB) Errorf(string, ...any) { c.failed.Store(true) }
func (c *captureTB) Error(...any)          { c.failed.Store(true) }
func (c *captureTB) Fatalf(string, ...any) { c.failed.Store(true) }
func (c *captureTB) Fatal(...any)          { c.failed.Store(true) }
func (c *captureTB) FailNow()              { c.failed.Store(true) }
func (c *captureTB) Fail()                 { c.failed.Store(true) }
func (c *captureTB) Failed() bool          { return c.failed.Load() }

// A Fails and an ordinary Check run in parallel tests in any real suite.
// Fails used to change rapid's process-wide nofailfile and shrinktime flags
// while the Check read them, which -race reports as a data race. It now
// records a violation without failing rapid, so this must stay race-free.
func TestFailsRacesAConcurrentCheck(t *testing.T) {
	rule := spec.Rule{ID: "C9", Kind: spec.Never, Status: spec.Established,
		Statement: "The counter has a value it can never hold."}
	m := counter(rule)
	m.Invariants = []spec.Invariant[int]{{Rule: rule, Check: func(int) spec.Verdict {
		return spec.Check(false, "nope")
	}}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 200 {
			spectest.Fails(t, counter(unreachable()).Check)
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			tb := &captureTB{}
			rapid.Check(tb, func(rt *rapid.T) { m.Check(rt) })
		}
	}()
	wg.Wait()
}
