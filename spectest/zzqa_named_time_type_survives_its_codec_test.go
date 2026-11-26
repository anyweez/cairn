package spectest_test

// A type whose underlying type is time.Time and which keeps its instant through
// its own JSON methods is a value the codec and the ledger store and read back
// unchanged: codec.CheckStorable accepts it (it checks a json.Marshaler through
// the bytes it returns), and json.Marshal/json.Unmarshal keep the instant. The
// checker's round-trip comparison walks the Go value instead, and a decoded
// time has a new *Location, so D6 and D7 report a change that did not happen.
//
// This is the time.Time case of t3086, on a shape that fix did not reach: the
// comparison recognises exactly time.Time by type, and a named time type (or a
// struct embedding one) reaches the generic struct walk, which compares the
// unexported wall/ext/loc fields with Go's ==.

import (
	"encoding/json"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// when is what a caller writes to attach a marshalling rule to a time —
// a named time type with methods that delegate to time.Time.
type when time.Time

func (w when) MarshalJSON() ([]byte, error) { return time.Time(w).MarshalJSON() }

func (w *when) UnmarshalJSON(data []byte) error {
	var t time.Time
	if err := json.Unmarshal(data, &t); err != nil {
		return err
	}
	*w = when(t)
	return nil
}

type stampedWhen struct {
	At when
	// Memo is dropped by encoding, so it is the control: a value that really
	// does lose something must still be reported.
	Memo string `json:"-"`
}

type whenMsg interface{ isWhenMsg() }

func (stampedWhen) isWhenMsg() {}

func whenCodecs() *codec.Set[whenMsg, int, struct{}, struct{}] {
	msgs := codec.New[whenMsg]()
	codec.Register[stampedWhen](msgs, "qa.when", 1)
	return &codec.Set[whenMsg, int, struct{}, struct{}]{Messages: msgs}
}

// qaWhen is an instant in a named zone, so it carries neither the process's
// zone pointer nor a monotonic reading: what a decoded time keeps is exactly
// the instant, which is all the codec stores.
func qaWhen() when {
	return when(time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*60*60+30*60)))
}

func whenMachine(msg stampedWhen) spectest.Machine[int, whenMsg, int, struct{}, struct{}] {
	return spectest.Machine[int, whenMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[int, whenMsg, int, struct{}, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(n int, _ whenMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](n+1, nil)
			},
			Evolve: func(n int, _ struct{}) int { return n },
		},
		Messages: func(t *rapid.T, _ int) whenMsg { return msg },
		Codecs:   whenCodecs(),
	}
}

// D6 (Encodable): the message survives its codec, so the check must pass.
func TestQANamedTimeMessageSurvivesItsCodec(t *testing.T) {
	msg := stampedWhen{At: qaWhen()}
	if err := codec.CheckStorable(msg); err != nil {
		t.Fatalf("premise: the ledger's own storable check refuses the message: %v", err)
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("premise: the message cannot be encoded: %v", err)
	}
	var back stampedWhen
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("premise: the message cannot be decoded: %v", err)
	}
	if !time.Time(back.At).Equal(time.Time(msg.At)) {
		t.Fatalf("premise: the round trip moved the instant: %v became %v", msg.At, back.At)
	}

	if out, failed, err := spectest.Fails(t, whenMachine(msg).Check); err != nil || failed {
		t.Fatalf("a message whose instant the codec keeps must not fail D6: err=%v failed=%v\n%s", err, failed, out)
	}
}

// D7 (Snapshottable): the same, for a state holding the named time.
func TestQANamedTimeStateSurvivesItsSnapshot(t *testing.T) {
	state := stampedWhen{At: qaWhen()}
	if err := codec.CheckStorable(state); err != nil {
		t.Fatalf("premise: the ledger's own storable check refuses the state: %v", err)
	}

	m := spectest.Machine[stampedWhen, int, int, struct{}, struct{}]{
		Decider: cairn.Decider[stampedWhen, int, int, struct{}, struct{}]{
			Initial: func() stampedWhen { return state },
			Decide: func(s stampedWhen, _ int) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](0, nil)
			},
			Evolve: func(s stampedWhen, _ struct{}) stampedWhen { return s },
		},
		Messages:  func(t *rapid.T, _ stampedWhen) int { return 0 },
		Snapshots: true,
	}

	if out, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("a state whose instant the snapshot keeps must not fail D7: err=%v failed=%v\n%s", err, failed, out)
	}
}

// The control: a message the codec really does lose something from is still
// reported, so a fix cannot make D6 accept everything.
func TestQANamedTimeMessageDroppingAFieldStillFailsD6(t *testing.T) {
	if out, failed, err := spectest.Fails(t, whenMachine(stampedWhen{At: qaWhen(), Memo: "rent"}).Check); err != nil || !failed {
		t.Fatalf("a field the codec drops must still fail D6: err=%v failed=%v\n%s", err, failed, out)
	}
}
