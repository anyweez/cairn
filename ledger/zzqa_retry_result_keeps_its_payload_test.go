package ledger_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// qaRetryPayload is a result whose JSON text is not what jsonb stores: an
// opaque payload keeps its key order, a json.Number keeps the text the caller
// wrote, and a float64 keeps the sign of zero. None of the three survives
// Postgres parsing the value and re-serialising it, which is what the entry
// columns do.
type qaRetryPayload struct {
	Raw json.RawMessage `json:"raw"`
	Num json.Number     `json:"num"`
	Neg float64         `json:"neg"`
}

// An IdempotencyKey "returns the first answer" (README, "Safe retries"), and
// Handled.Result says the first call, the stored entry and every retry return
// the same value. A retry is answered by decoding the stored result, which
// passes through the result column; when that column rewrites the bytes, the
// retry returns a different answer from the first call and nothing reports it.
func TestARetryReturnsTheResultTheFirstCallDid(t *testing.T) {
	msgs := codec.New[msg]()
	codec.Register[add](msgs, "add", 1)
	results := codec.New[qaRetryPayload]()
	codec.Register[qaRetryPayload](results, "payload", 1)
	events := codec.New[event]()
	codec.Register[added](events, "added", 1)
	effects := codec.New[effect]()
	codec.Register[announce](effects, "announce", 1)

	want := qaRetryPayload{
		Raw: json.RawMessage(`{"b":2,"a":1}`),
		Num: json.Number("1e2"),
		Neg: math.Copysign(0, -1),
	}
	agg := &ledger.Aggregate[counter, msg, qaRetryPayload, event, effect]{
		Store: ledgertest.NewStore(t),
		Kind:  "qa-retry",
		Decider: cairn.Decider[counter, msg, qaRetryPayload, event, effect]{
			Initial: func() counter { return counter{} },
			Decide: func(counter, msg) cairn.Decision[qaRetryPayload, event, effect] {
				return cairn.Accept[qaRetryPayload, event, effect](want, nil)
			},
			Evolve: func(s counter, e event) counter { return s },
		},
		Codecs: &codec.Set[msg, qaRetryPayload, event, effect]{
			Messages: msgs, Results: results, Events: events, Effects: effects,
		},
	}

	first, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Result.Raw) != string(want.Raw) || first.Result.Num != want.Num || math.Signbit(first.Result.Neg) != math.Signbit(want.Neg) {
		t.Fatalf("the first call must return the value the decider returned, got %+v", first.Result)
	}

	again, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate {
		t.Fatalf("the second call must be answered as a retry: %+v", again)
	}
	if string(again.Result.Raw) != string(first.Result.Raw) {
		t.Errorf("the retry returned an opaque JSON payload of %s, but the first call returned %s: the result column rewrote the bytes",
			again.Result.Raw, first.Result.Raw)
	}
	if again.Result.Num != first.Result.Num {
		t.Errorf("the retry returned the number %q, but the first call returned %q", again.Result.Num, first.Result.Num)
	}
	if math.Signbit(again.Result.Neg) != math.Signbit(first.Result.Neg) {
		t.Errorf("the retry returned %v, but the first call returned %v", again.Result.Neg, first.Result.Neg)
	}
}
