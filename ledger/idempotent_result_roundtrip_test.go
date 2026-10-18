package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The README promises that an IdempotencyKey "returns the first answer". For
// an accepted decision, queueEntry hands the first caller the decider's
// Result before it is encoded, while a retry reads the stored result back
// through Results. When R does not survive a JSON round trip, the two answers
// differ: map[string]any{"n": 1} has an int on the first call and a float64 on
// the retry. The first answer must be the decoded, stored one, so the first
// call, the entry and every retry carry the same value.
func TestAnAcceptedRetryDecodesTheResultSoTheTwoCallsCanDisagree(t *testing.T) {
	msgs := codec.New[msg]()
	codec.Register[add](msgs, "add", 1)
	results := codec.New[map[string]any]()
	codec.Register[map[string]any](results, "payload", 1)

	agg := &ledger.Aggregate[counter, msg, map[string]any, event, effect]{
		Store: ledgertest.NewStore(t),
		Kind:  "roundtrip",
		Decider: cairn.Decider[counter, msg, map[string]any, event, effect]{
			Initial: func() counter { return counter{} },
			Decide: func(counter, msg) cairn.Decision[map[string]any, event, effect] {
				return cairn.Accept[map[string]any, event, effect](map[string]any{"n": 1}, nil)
			},
			Evolve: func(s counter, e event) counter { return s },
		},
		Codecs: &codec.Set[msg, map[string]any, event, effect]{
			Messages: msgs,
			Results:  results,
			Events:   codec.New[event](),
			Effects:  codec.New[effect](),
		},
	}

	first, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate {
		t.Fatalf("the second call must be answered as a retry: %+v", again)
	}
	if first.Result["n"] != again.Result["n"] {
		t.Fatalf("the same message and key answered differently: first %T(%v), retry %T(%v)",
			first.Result["n"], first.Result["n"], again.Result["n"], again.Result["n"])
	}
}
