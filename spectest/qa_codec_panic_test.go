package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// qaPanicMsg is a message whose own JSON encoding panics. CheckStorable
// treats a json.Marshaler as opaque and lets json.Marshal call it, so the
// panic happens inside codec.Encode, under the D6 (Encodable) round trip.
type qaPanicMsg struct{}

func (qaPanicMsg) isQAMsg() {}
func (qaPanicMsg) MarshalJSON() ([]byte, error) {
	panic("qa: the message cannot marshal itself")
}

type qaMsg interface{ isQAMsg() }

func qaCodecs() *codec.Set[qaMsg, int, struct{}, struct{}] {
	msgs := codec.New[qaMsg]()
	codec.Register[qaPanicMsg](msgs, "qa.panic", 1)
	results := codec.New[int]()
	codec.Register[int](results, "qa.result", 1)
	events := codec.New[struct{}]()
	codec.Register[struct{}](events, "qa.event", 1)
	effects := codec.New[struct{}]()
	codec.Register[struct{}](effects, "qa.effect", 1)
	return &codec.Set[qaMsg, int, struct{}, struct{}]{Messages: msgs, Results: results, Events: events, Effects: effects}
}

// A panic while round-tripping a message must be reported as the rule the
// round trip is judged under (D6), not escape as an unnamed rapid harness
// panic. Every other check in spectest guards user code with checked/guard
// for exactly this reason.
func TestQACodecPanicNamesTheRule(t *testing.T) {
	m := spectest.Machine[struct{}, qaMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[struct{}, qaMsg, int, struct{}, struct{}]{
			Initial: func() struct{} { return struct{}{} },
			Decide: func(s struct{}, msg qaMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](0, nil)
			},
			Evolve: func(s struct{}, ev struct{}) struct{} { return s },
		},
		Messages: func(t *rapid.T, _ struct{}) qaMsg { return qaPanicMsg{} },
		Codecs:   qaCodecs(),
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Encodable.ID+" violated") {
		t.Fatalf("a panicking codec must be reported as %s, got err=%v failed=%v: %s", spectest.Encodable.ID, err, failed, msg)
	}
}
