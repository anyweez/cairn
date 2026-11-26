package spectest_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// qaSealed keeps its time in an unexported field and writes it out itself.
// This is the ordinary way an application keeps a message's fields private
// while still storing the value: json only reaches the time through the
// type's own encoding, and the ledger keeps it.
type qaSealed struct{ at time.Time }

func (m qaSealed) MarshalJSON() ([]byte, error) { return json.Marshal(m.at) }

func (m *qaSealed) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &m.at) }

func (qaSealed) isQASealed() {}

type qaSealedMsg interface{ isQASealed() }

// qaSealedState is the same shape as a state: the ledger snapshots it through
// json.Marshal, which calls the type's own encoding.
type qaSealedState struct{ at time.Time }

func (s qaSealedState) MarshalJSON() ([]byte, error) { return json.Marshal(s.at) }

func (s *qaSealedState) UnmarshalJSON(data []byte) error { return json.Unmarshal(data, &s.at) }

// qaSealedAt is a time in a named zone, the shape the codec keeps as the same
// instant and the ledger stores (codec/storable_time_test.go lists it).
func qaSealedAt() time.Time {
	return time.Date(2026, 3, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*3600+30*60))
}

func qaSealedCodecs() *codec.Set[qaSealedMsg, int, struct{}, struct{}] {
	msgs := codec.New[qaSealedMsg]()
	codec.Register[qaSealed](msgs, "sealed", 1)
	return &codec.Set[qaSealedMsg, int, struct{}, struct{}]{Messages: msgs}
}

func qaSealedMachine() spectest.Machine[qaSealedState, qaSealedMsg, int, struct{}, struct{}] {
	return spectest.Machine[qaSealedState, qaSealedMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[qaSealedState, qaSealedMsg, int, struct{}, struct{}]{
			Initial: func() qaSealedState { return qaSealedState{} },
			Decide: func(s qaSealedState, m qaSealedMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](1, nil)
			},
			Evolve: func(s qaSealedState, _ struct{}) qaSealedState { return s },
		},
		Messages: func(t *rapid.T, _ qaSealedState) qaSealedMsg {
			return qaSealed{at: qaSealedAt()}
		},
		Codecs:    qaSealedCodecs(),
		Snapshots: true,
	}
}

// A message whose time lives in an unexported field, written out by the
// type's own JSON encoding, comes back from the codec as the same instant.
// The ledger stores it (codec.CheckStorable accepts a json.Marshaler through
// its bytes), so the spec check must not report the round trip as a change.
func TestQAAnUnexportedTimeFieldSurvivesItsCodec(t *testing.T) {
	msg := qaSealed{at: qaSealedAt()}
	if err := codec.CheckStorable(msg); err != nil {
		t.Fatalf("premise: the message must be storable, got %v", err)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("premise: %v", err)
	}
	var back qaSealed
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("premise: %v", err)
	}
	if !back.at.Equal(msg.at) {
		t.Fatalf("premise: the round trip moved the instant, %v → %v", msg.at, back.at)
	}

	got, failed, err := spectest.Fails(t, qaSealedMachine().Check)
	if err != nil {
		t.Fatalf("the check died: %v\n%s", err, got)
	}
	if failed && strings.Contains(got, spectest.Encodable.ID) {
		t.Fatalf("a message the codec carries unchanged was failed by %s:\n%s", spectest.Encodable.ID, got)
	}
	if failed {
		t.Fatalf("the machine failed for some other reason:\n%s", got)
	}
}

// The same shape as a snapshotted state: the ledger writes it with
// json.Marshal and reads it back as the same instant, so D7 must not report
// it as changed.
func TestQAAnUnexportedTimeFieldSurvivesItsSnapshot(t *testing.T) {
	state := qaSealedState{at: qaSealedAt()}
	if err := codec.CheckStorable(state); err != nil {
		t.Fatalf("premise: the state must be storable, got %v", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("premise: %v", err)
	}
	var back qaSealedState
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("premise: %v", err)
	}
	if !back.at.Equal(state.at) {
		t.Fatalf("premise: the round trip moved the instant, %v → %v", state.at, back.at)
	}

	m := qaSealedMachine()
	m.Decider.Initial = func() qaSealedState { return state }
	m.Codecs = nil
	got, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check died: %v\n%s", err, got)
	}
	if failed && strings.Contains(got, spectest.Snapshottable.ID) {
		t.Fatalf("a state the snapshot carries unchanged was failed by %s:\n%s", spectest.Snapshottable.ID, got)
	}
	if failed {
		t.Fatalf("the machine failed for some other reason:\n%s", got)
	}
}

// The control: a time in an unexported field that the type's own encoding
// does not write must still be reported, so a fix cannot make the comparison
// accept every unexported time.
func TestQAAnUnexportedTimeFieldTheCodecDropsStillFailsD6(t *testing.T) {
	m := qaSealedMachine()
	// The state is not what this control is about: it holds the same
	// unexported time, which D7 would report before D6 is reached.
	m.Snapshots = false
	m.Messages = func(t *rapid.T, _ qaSealedState) qaSealedMsg {
		// A message that does not carry its time at all.
		return qaSealedNoTime{}
	}
	got, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check died: %v\n%s", err, got)
	}
	if !failed || !strings.Contains(got, spectest.Encodable.ID) {
		t.Fatalf("a message field the codec drops must still fail %s:\n%s", spectest.Encodable.ID, got)
	}
}

// qaSealedNoTime is a message of the same sum type whose unexported time is
// never written, so the round trip really does lose it.
type qaSealedNoTime struct{ at time.Time }

func (qaSealedNoTime) isQASealed() {}

func (m qaSealedNoTime) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }
