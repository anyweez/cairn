package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn"
)

// Verify exists to report where today's code disagrees with what a stream
// recorded. It compares a stored result with the re-decided one by decoding
// both through JSON into `any`, which turns every number into a float64. Two
// int64 results that differ but round to the same float64 therefore compare
// equal, and the divergence is silently missed.
//
// A result is often an int64 that can exceed float64's exact range: a count, a
// duration, a timestamp in nanoseconds. A stored 2^53+1 against a current 2^53
// is the smallest case.
func TestVerifySpotsAResultThatDiffersBeyondFloatPrecision(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	base := agg.Decider.Decide
	const big = int64(1) << 53

	// Record an accepted message whose result is big+1.
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		d := base(s, m)
		d.Result = int(big) + 1
		return d
	}
	if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}

	// Today's code returns big instead, one less. Verify must report it.
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		d := base(s, m)
		d.Result = int(big)
		return d
	}
	divergences, err := agg.Verify(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(divergences) == 0 {
		t.Fatalf("Verify missed that the stored result %d is now %d", big+1, big)
	}
}
