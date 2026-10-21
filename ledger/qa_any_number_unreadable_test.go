package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A registered event may carry an `any` field. codec.CheckStorable sees a
// json.Number as text and lets it through, json.Marshal writes it, and the
// write succeeds. On the way back, codec.Decode hands the field to
// encoding/json, which turns a JSON number into a float64 and fails on a
// number outside float64's range. The event is stored but the stream can no
// longer be folded, so Load and every non-cached Handle after it fail.

type anyState struct{ N int }

type anyMsg interface{ isAnyMsg() }

type anyAdd struct{ Payload any }

func (anyAdd) isAnyMsg() {}

type anyEvent interface{ isAnyEvent() }

type anyAdded struct{ Payload any }

func (anyAdded) isAnyEvent() {}

type anyEffect interface{ isAnyEffect() }

type anyNoop struct{}

func (anyNoop) isAnyEffect() {}

var anyDecider = cairn.Decider[anyState, anyMsg, int, anyEvent, anyEffect]{
	Initial: func() anyState { return anyState{} },
	Decide: func(s anyState, m anyMsg) cairn.Decision[int, anyEvent, anyEffect] {
		a := m.(anyAdd)
		return cairn.Accept[int, anyEvent, anyEffect](s.N+1, []anyEvent{anyAdded{Payload: a.Payload}})
	},
	Evolve: func(s anyState, _ anyEvent) anyState { s.N++; return s },
}

func anyCodecs() *codec.Set[anyMsg, int, anyEvent, anyEffect] {
	msgs := codec.New[anyMsg]()
	codec.Register[anyAdd](msgs, "any.add", 1)
	events := codec.New[anyEvent]()
	codec.Register[anyAdded](events, "any.added", 1)
	effects := codec.New[anyEffect]()
	codec.Register[anyNoop](effects, "any.noop", 1)
	results := codec.New[int]()
	codec.Register[int](results, "any.count", 1)
	return &codec.Set[anyMsg, int, anyEvent, anyEffect]{Messages: msgs, Results: results, Events: events, Effects: effects}
}

func TestAnEventsAnyNumberTooLargeForFloat64LeavesTheStreamUnreadable(t *testing.T) {
	agg := &ledger.Aggregate[anyState, anyMsg, int, anyEvent, anyEffect]{
		Store: ledgertest.NewStore(t), Kind: "any", Decider: anyDecider, Codecs: anyCodecs(),
	}
	h, err := agg.Handle(ctx, "a", anyAdd{Payload: json.Number("1e400")})
	if err != nil {
		t.Fatalf("the write must be accepted: %v", err)
	}
	if h.Result != 1 {
		t.Fatalf("result %d, want 1", h.Result)
	}
	if _, err := agg.Load(ctx, "a"); err != nil {
		t.Fatalf("the stream cannot be loaded after a successful write: %v", err)
	}
}
