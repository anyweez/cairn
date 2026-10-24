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

// A result is stored in cairn.entries.result, a jsonb column, and an
// idempotent retry decodes it back out. jsonb does not keep the bytes
// json.Marshal wrote: Postgres parses the JSON and re-serialises it, which
// reorders object keys and normalises numbers. So a result carrying an opaque
// JSON payload, a json.Number or a negative zero is answered one way the first
// time and another way on every retry.
//
// Handled.Result promises the opposite: "the first call, the stored entry and
// every idempotent retry all return this same value, even when R does not
// survive a JSON round trip unchanged." It survives the Go round trip; it does
// not survive the column the entry is written to.
type qaJSON struct {
	Raw json.RawMessage `json:"raw"`
	Num json.Number     `json:"num"`
	Neg float64         `json:"neg"`
}

type qaJSONState struct{}

type qaJSONMsg struct{ Payload qaJSON }

type qaJSONEvent struct{}

type qaJSONEffect struct{}

var qaJSONDecider = cairn.Decider[qaJSONState, qaJSONMsg, qaJSON, qaJSONEvent, qaJSONEffect]{
	Initial: func() qaJSONState { return qaJSONState{} },
	Decide: func(_ qaJSONState, m qaJSONMsg) cairn.Decision[qaJSON, qaJSONEvent, qaJSONEffect] {
		return cairn.Accept[qaJSON, qaJSONEvent, qaJSONEffect](m.Payload, nil)
	},
	Evolve: func(s qaJSONState, _ qaJSONEvent) qaJSONState { return s },
}

func qaJSONAggregate(t *testing.T) *ledger.Aggregate[qaJSONState, qaJSONMsg, qaJSON, qaJSONEvent, qaJSONEffect] {
	t.Helper()
	messages := codec.New[qaJSONMsg]()
	codec.Register[qaJSONMsg](messages, "qa.json.message", 1)
	results := codec.New[qaJSON]()
	codec.Register[qaJSON](results, "qa.json.result", 1)
	events := codec.New[qaJSONEvent]()
	codec.Register[qaJSONEvent](events, "qa.json.event", 1)
	effects := codec.New[qaJSONEffect]()
	codec.Register[qaJSONEffect](effects, "qa.json.effect", 1)
	return &ledger.Aggregate[qaJSONState, qaJSONMsg, qaJSON, qaJSONEvent, qaJSONEffect]{
		Store: ledgertest.NewStore(t), Kind: "qa-json", Decider: qaJSONDecider,
		Codecs: &codec.Set[qaJSONMsg, qaJSON, qaJSONEvent, qaJSONEffect]{
			Messages: messages, Results: results, Events: events, Effects: effects,
		},
	}
}

func TestAnIdempotentRetryReturnsTheResultTheFirstCallDid(t *testing.T) {
	agg := qaJSONAggregate(t)
	sent := qaJSON{
		Raw: json.RawMessage(`{"b":2,"a":1}`),
		Num: json.Number("1e2"),
		Neg: math.Copysign(0, -1),
	}

	first, err := agg.Handle(ctx, "a", qaJSONMsg{Payload: sent}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	retry, err := agg.Handle(ctx, "a", qaJSONMsg{Payload: sent}, ledger.IdempotencyKey("k"))
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate {
		t.Fatalf("the second call was not answered from the first entry: %+v", retry)
	}

	if got := string(first.Result.Raw); got != string(sent.Raw) {
		t.Fatalf("the first call's result is %s, not the payload the decider returned (%s)", got, sent.Raw)
	}
	if got := string(retry.Result.Raw); got != string(first.Result.Raw) {
		t.Errorf("the retry returned an opaque JSON payload of %s, but the first call returned %s: the result column rewrote the bytes",
			got, first.Result.Raw)
	}
	if retry.Result.Num != first.Result.Num {
		t.Errorf("the retry returned the number %q, but the first call returned %q: the result column normalised it",
			retry.Result.Num, first.Result.Num)
	}
	if math.Signbit(retry.Result.Neg) != math.Signbit(first.Result.Neg) {
		t.Errorf("the retry returned %v, but the first call returned %v: the result column lost the sign of zero",
			retry.Result.Neg, first.Result.Neg)
	}
}
