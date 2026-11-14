package ledger_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// Verify's contract is to re-decide every stored message and report each
// divergence, rebuilding state from the stored events so one divergence does
// not cascade. A panic in today's Decider is a failure in the middle of that
// audit: it must be reported for the entry it happened at, and the divergences
// already found must survive. The write path (aggregate.record) already
// recovers a decider panic and reports it to the caller rather than ending the
// process.
func TestQA_VerifyReportsADeciderPanicAtTheEntryAndKeepsEarlierDivergences(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)

	// Two stored entries, decided cleanly by the code that wrote them.
	for i := 0; i < 2; i++ {
		if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
			t.Fatal(err)
		}
	}

	// Today's decider diverges on the first entry and panics on the second, so
	// Verify has one divergence in hand when the panic happens.
	calls := 0
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		calls++
		if calls == 1 {
			d := decide(s, m)
			d.Result = 424242
			return d
		}
		panic("qa: today's decider cannot decide a message it used to")
	}

	var (
		divergences []ledger.Divergence
		err         error
	)
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("Verify let a panic in the decider escape and lost the %d divergence(s) it had already found: %v",
					len(divergences), p)
			}
		}()
		divergences, err = agg.Verify(ctx, "a")
	}()

	if len(divergences) == 0 {
		t.Fatalf("Verify lost the divergence it found on a#1 before the panic; err=%v", err)
	}
	if divergences[0].Stream.ID != "a" || divergences[0].Seq != 1 || divergences[0].Aspect != ledger.Returned {
		t.Fatalf("Verify reported %v before the panic; want the a#1 %s divergence", divergences, ledger.Returned)
	}

	// The panic itself has to be reported for the entry it happened at. A fix
	// may return it as an error alongside the divergences, or as a divergence
	// of its own; either way the report names a#2.
	reported := err != nil && strings.Contains(err.Error(), "a#2")
	if err == nil && len(divergences) > 1 && divergences[1].Stream.ID == "a" && divergences[1].Seq == 2 {
		reported = true
	}
	if !reported {
		t.Fatalf("Verify reported %v and err=%v; the decider panic at a#2 is not named", divergences, err)
	}
}
