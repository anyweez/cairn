package ledger_test

import (
	"context"
	"sync"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// A refusal may carry a non-zero Result. Two callers sending the same message
// with the same idempotency key at once must get the same answer: the first
// caller's Result, not the zero value the stored refusal cannot reproduce.
func TestQAConcurrentRetryOfARefusalGetsTheSameResult(t *testing.T) {
	agg := newCounter(t)
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		return cairn.Decision[int, event, effect]{
			Result:  99,
			Refusal: &cairn.Refusal{Code: "over_cap", Message: "no"},
		}
	}

	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Go(func() {
			h, err := agg.Handle(context.Background(), "a", add{1, 10}, ledger.IdempotencyKey("k"))
			results[i], errs[i] = h.Result, err
		})
	}
	wg.Wait()
	for i := range 2 {
		var refusal *cairn.Refusal
		if !asRefusal(errs[i], &refusal) {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
	}
	if results[0] != results[1] {
		t.Fatalf("the same message and key answered differently: %d and %d", results[0], results[1])
	}
}

func asRefusal(err error, out **cairn.Refusal) bool {
	r, ok := err.(*cairn.Refusal)
	if ok {
		*out = r
	}
	return ok
}
