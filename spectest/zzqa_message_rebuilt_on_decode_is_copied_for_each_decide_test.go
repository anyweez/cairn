package spectest_test

// The ledger encodes the caller's message before Decide sees it and hands
// Decide the value decoded back from that envelope, so every Decide call
// starts from an independent copy of the message as sent
// (ledger/message_as_sent_test.go is that contract). spectest judges D1 by
// calling Decide twice, each with what it believes is such a copy, and D6 by
// round-tripping the message as sent (spectest/clone.go, Machine.run).
//
// clone deep-copies a reference reached through an unexported field only when
// the struct's own JSON *encoding* writes it, i.e. when the type implements
// json.Marshaler or encoding.TextMarshaler (spectest/clone.go's encodesItself,
// added for t3480). A type that implements only json.Unmarshaler — the
// ordinary way to keep a derived index private and rebuild it from the exported
// fields on read — still shares that reference between the checker's two
// Decide calls, and Decide writes through it into the message the checker
// round-trips as "the message as sent". The ledger is unaffected: it encodes
// the caller's message first and decodes a fresh value per call.
//
// So a decider the ledger supports is failed by D1, or by D6 with a message it
// never sent, depending on whether its normalisation is idempotent.

import (
	"encoding/json"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// zzqaDerivedMsg carries an index built from its exported words. It has no
// MarshalJSON: the index is derived, rebuilt by UnmarshalJSON, and never
// stored.
type zzqaDerivedMsg struct {
	Words []string `json:"words"`
	index map[string]int
}

// zzqaDerivedIndex is the index a decode builds for a message, so the message
// a generator sends carries the same index its own round trip would give it.
func zzqaDerivedIndex(words []string) map[string]int {
	index := make(map[string]int, len(words)+1)
	for i, w := range words {
		index[w] = i
	}
	return index
}

func (m *zzqaDerivedMsg) UnmarshalJSON(data []byte) error {
	var wire struct {
		Words []string `json:"words"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	m.Words = wire.Words
	m.index = zzqaDerivedIndex(wire.Words)
	return nil
}

func (zzqaDerivedMsg) isZZQADerived() {}

type zzqaDerived interface{ isZZQADerived() }

func zzqaDerivedCodecs() *codec.Set[zzqaDerived, int, struct{}, struct{}] {
	msgs := codec.New[zzqaDerived]()
	codec.Register[zzqaDerivedMsg](msgs, "zzqa.derived.msg", 1)
	return &codec.Set[zzqaDerived, int, struct{}, struct{}]{Messages: msgs}
}

// zzqaDerivedCounting normalises the derived index in place and returns how
// many calls it has recorded there. Over a freshly decoded message that is 1
// on both calls; over an index shared between the two calls the second one
// sees the first one's write and returns 2.
func zzqaDerivedCounting(_ struct{}, m zzqaDerived) cairn.Decision[int, struct{}, struct{}] {
	msg := m.(zzqaDerivedMsg)
	calls := msg.index["calls"] + 1
	msg.index["calls"] = calls
	return cairn.Accept[int, struct{}, struct{}](calls, nil)
}

// zzqaDerivedMarking normalises the derived index in place idempotently: the
// value it returns is the same on the second call, but the message it was
// given has been rewritten.
func zzqaDerivedMarking(_ struct{}, m zzqaDerived) cairn.Decision[int, struct{}, struct{}] {
	msg := m.(zzqaDerivedMsg)
	msg.index["seen"] = 1
	return cairn.Accept[int, struct{}, struct{}](len(msg.index), nil)
}

func zzqaDerivedMachine(decide func(struct{}, zzqaDerived) cairn.Decision[int, struct{}, struct{}]) spectest.Machine[struct{}, zzqaDerived, int, struct{}, struct{}] {
	return spectest.Machine[struct{}, zzqaDerived, int, struct{}, struct{}]{
		Decider: cairn.Decider[struct{}, zzqaDerived, int, struct{}, struct{}]{
			Initial: func() struct{} { return struct{}{} },
			Decide:  decide,
			Evolve:  func(s struct{}, _ struct{}) struct{} { return s },
		},
		Messages: func(t *rapid.T, _ struct{}) zzqaDerived {
			words := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,4}`), 0, 3).Draw(t, "words")
			return zzqaDerivedMsg{Words: words, index: zzqaDerivedIndex(words)}
		},
		Codecs: zzqaDerivedCodecs(),
	}
}

// The premise of the two tests below: the codec stores this message, and two
// decodes of one envelope build two independent indexes, which is what the
// ledger hands each Decide call. If either failed, the decider would not be one
// the ledger supports.
func TestTwoDecodesOfOneEnvelopeBuildIndependentIndexes(t *testing.T) {
	set := zzqaDerivedCodecs()
	original := zzqaDerivedMsg{Words: []string{"rent"}, index: zzqaDerivedIndex([]string{"rent"})}
	if err := codec.CheckStorable(original); err != nil {
		t.Fatalf("the ledger refuses this message, so it is not the case under test: %v", err)
	}
	env, err := set.Messages.Encode(original)
	if err != nil {
		t.Fatalf("encoding the message: %v", err)
	}
	first, err := set.Messages.Decode(env)
	if err != nil {
		t.Fatalf("decoding the message: %v", err)
	}
	second, err := set.Messages.Decode(env)
	if err != nil {
		t.Fatalf("decoding the message: %v", err)
	}
	// A write through one decoded message reaches the other only if the codec
	// handed out one shared value.
	first.(zzqaDerivedMsg).index["seen"] = 1
	if _, ok := second.(zzqaDerivedMsg).index["seen"]; ok {
		t.Fatalf("two decodes of one envelope share an index")
	}
}

// The first symptom: D1 calls the decider nondeterministic because its second
// call is handed the message the first one rewrote. The ledger decides this
// machine the same way on every call made from the message as sent.
func TestANormalisingMessageRebuiltOnDecodeIsNotNondeterministic(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaDerivedMachine(zzqaDerivedCounting).Check)
	if err != nil {
		t.Fatalf("the check died instead of reporting a verdict: %v", err)
	}
	if failed {
		t.Fatalf("the ledger hands each Decide an independent message, so D1 must not fail this machine:\n%s", msg)
	}
}

// The second symptom: the normalisation is idempotent, so D1 is happy, but the
// write reached the message the checker round-trips as the one the caller sent,
// and D6 reports a message that was never sent.
func TestAMessageRebuiltOnDecodeSurvivesItsCodec(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaDerivedMachine(zzqaDerivedMarking).Check)
	if err != nil {
		t.Fatalf("the check died instead of reporting a verdict: %v", err)
	}
	if failed {
		t.Fatalf("that message survives its codec, so D6 must not fail this machine:\n%s", msg)
	}
}
