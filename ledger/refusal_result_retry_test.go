package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A refusal is stored without its result: queueEntry encodes a decision's
// Result only when the decision was accepted. But the first caller is handed
// the decision's Result anyway, because the finish closure returns
// decision.Result. A retry of the same idempotency key is answered from the
// stored entry, whose Result is nil, so the retry gets the zero R.
//
// The README promises that an IdempotencyKey "returns the first answer,
// refusals included", and Handled.Duplicate documents Result as "the earlier
// entry's". Neither holds here: the first answer is not the entry's, and the
// retry is not the first answer. The two differ on a refusal's Result even
// though the caller sent the same message with the same key.
//
// The framework's D3 forbids a refusal carrying events or effects but not a
// result, and Decision's own doc lists only events and effects, so a refusal
// that carries a result is legal input. The ledger must return the same
// Handled for both calls; either zero the Result of a refusal before returning
// it or record it, as it does for an accepted decision.
func TestAResultOnARefusalIsNotRecordedSoARetryAnswersDifferently(t *testing.T) {
	agg := counterOn(ledgertest.NewStore(t))
	agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
		d := cairn.Refuse[int, event, effect]("over_cap", "no")
		d.Result = 99
		return d
	}

	first, firstErr := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if firstErr == nil {
		t.Fatal("the decision refuses, so Handle must return a refusal")
	}
	again, againErr := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if againErr == nil || !again.Duplicate {
		t.Fatalf("the retry must be answered as a duplicate, got %+v, %v", again, againErr)
	}
	if first.Result != again.Result {
		t.Fatalf("the same message and key answered differently: first Result=%d, retry Result=%d", first.Result, again.Result)
	}
}
