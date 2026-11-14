package spectest_test

// The ledger encodes the caller's message before Decide sees it and gives
// Decide the message decoded back from that envelope, so a decider that
// normalises a field of its message in place is supported: each call gets a
// fresh copy of what the caller sent (ledger/message_as_sent_test.go is the
// contract).
//
// spectest judges D1 by calling Decide twice with what it believes are two
// independent copies of the message. clone copies every map, slice and pointer
// it reaches, except one reached through an unexported struct field: reflect
// cannot set that field, so the shallow struct copy leaves the reference
// shared. The clone doc says that is safe because "the codec that stores a
// message does not write an unexported field, and a non-zero one fails D6".
// That premise is false for a message type with its own MarshalJSON and
// UnmarshalJSON, which is the ordinary way to keep a message's fields private
// while still storing them: the codec writes the unexported field, the value
// round-trips, and the ledger really does hand Decide a fresh copy.
//
// This is the same defect as t3480 (a decider that normalises its message in
// place is reported as nondeterministic), on the one shape its fix (the D1
// clone in spectest/clone.go) does not reach.

import (
	"encoding/json"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// zzqaSeenMsg keeps its counter private and stores it through its own JSON
// encoding, so a value the ledger decoded carries a map independent of the one
// the caller sent.
type zzqaSeenMsg struct {
	seen map[string]int
	Note string
}

func (m zzqaSeenMsg) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Seen map[string]int `json:"seen"`
		Note string         `json:"note"`
	}{m.seen, m.Note})
}

func (m *zzqaSeenMsg) UnmarshalJSON(data []byte) error {
	var wire struct {
		Seen map[string]int `json:"seen"`
		Note string         `json:"note"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	m.seen = wire.Seen
	if m.seen == nil {
		m.seen = map[string]int{}
	}
	m.Note = wire.Note
	return nil
}

func (zzqaSeenMsg) isZZQASeen() {}

type zzqaSeen interface{ isZZQASeen() }

func zzqaSeenCodecs() *codec.Set[zzqaSeen, int, struct{}, struct{}] {
	msgs := codec.New[zzqaSeen]()
	codec.Register[zzqaSeenMsg](msgs, "zzqa.seen.msg", 1)
	return &codec.Set[zzqaSeen, int, struct{}, struct{}]{Messages: msgs}
}

// zzqaSeenDecide writes into the message it is given, the way a decider that
// normalises a caller's field in place does, and returns the count it wrote.
func zzqaSeenDecide(_ struct{}, m zzqaSeen) cairn.Decision[int, struct{}, struct{}] {
	msg := m.(zzqaSeenMsg)
	msg.seen["calls"]++
	return cairn.Accept[int, struct{}, struct{}](msg.seen["calls"], nil)
}

func zzqaSeenMachine() spectest.Machine[struct{}, zzqaSeen, int, struct{}, struct{}] {
	return spectest.Machine[struct{}, zzqaSeen, int, struct{}, struct{}]{
		Decider: cairn.Decider[struct{}, zzqaSeen, int, struct{}, struct{}]{
			Initial: func() struct{} { return struct{}{} },
			Decide:  zzqaSeenDecide,
			Evolve:  func(s struct{}, _ struct{}) struct{} { return s },
		},
		Messages: func(t *rapid.T, _ struct{}) zzqaSeen {
			return zzqaSeenMsg{seen: map[string]int{}, Note: "rent"}
		},
		Codecs: zzqaSeenCodecs(),
	}
}

// The ledger stores this message and gives each Decide a freshly decoded value,
// so the decider is deterministic over the input it is actually given. D1 must
// not fail it.
func TestANormalisingMessageWithAnUnexportedFieldIsNotNondeterministic(t *testing.T) {
	// The premise: the codec stores the message, and two decodes of one
	// envelope give two values whose unexported maps do not alias. If either
	// failed, the decider would not be one the ledger supports.
	set := zzqaSeenCodecs()
	original := zzqaSeenMsg{seen: map[string]int{}, Note: "rent"}
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
		t.Fatalf("decoding the message a second time: %v", err)
	}
	if got, want := zzqaSeenDecide(struct{}{}, first).Result, 1; got != want {
		t.Fatalf("the first fresh message counted %d, want %d", got, want)
	}
	if got, want := zzqaSeenDecide(struct{}{}, second).Result, 1; got != want {
		t.Fatalf("the second fresh message counted %d, want %d, so a decode does not give an independent map", got, want)
	}

	msg, failed, err := spectest.Fails(t, zzqaSeenMachine().Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed {
		t.Fatalf("a decider the ledger supports was failed by the spec check:\n%s", msg)
	}
}

// The other side of the rule: a decider whose answer depends on something
// outside its inputs is still nondeterministic, and D1 must catch it, even
// when its message keeps a field unexported.
func TestANormalisingDeciderStillCannotDependOnSomethingOutsideItsInputs(t *testing.T) {
	m := zzqaSeenMachine()
	calls := 0
	m.Decider.Decide = func(_ struct{}, msg zzqaSeen) cairn.Decision[int, struct{}, struct{}] {
		calls++
		return cairn.Accept[int, struct{}, struct{}](calls, nil)
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Deterministic.ID+" violated") {
		t.Fatalf("a decider that depends on a call count must fail %s, got err=%v failed=%v:\n%s",
			spectest.Deterministic.ID, err, failed, msg)
	}
}
