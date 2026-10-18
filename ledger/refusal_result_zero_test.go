package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A refusal stores no result (Entry.Result is nil when the message was
// refused), so the first call must answer with the zero R rather than the
// decider's. This pins the direction of the fix: the two QA reproduction tests
// only require the first and retried results to agree, which recording the
// result would also satisfy.
func TestARefusalAnswersTheZeroResult(t *testing.T) {
	agg := counterOn(ledgertest.NewStore(t))
	agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
		d := cairn.Refuse[int, event, effect]("over_cap", "no")
		d.Result = 99
		return d
	}

	first, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err == nil {
		t.Fatal("the decision refuses, so Handle must return a refusal")
	}
	if first.Result != 0 {
		t.Fatalf("a refusal is stored without a result, so the first answer must be the zero R, got %d", first.Result)
	}

	entries, _, err := agg.Store.ReadAll(ctx, ledger.Cursor{}, 10, "counter")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one recorded entry, got %d", len(entries))
	}
	if entries[0].Refusal == nil {
		t.Fatal("the recorded entry must be the refusal")
	}
	if entries[0].Result != nil {
		t.Fatalf("a refusal's stored Result must be nil, got %+v", *entries[0].Result)
	}
}
