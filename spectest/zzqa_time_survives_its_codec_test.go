package spectest_test

// A time.Time carries two things JSON does not: a monotonic clock reading, and
// the *Location its zone offset came from. json.Marshal writes neither, and the
// codec's own answer to "does this survive the ledger" (codec.CheckStorable,
// which queueSnapshot runs before it writes a snapshot) accepts a value whose
// round trip is the same instant, with or without them. spectest's D6 and D7
// compare the round trip with reflect.DeepEqual instead, so a message or state
// carrying the current time, or a time in a named zone, is reported as changed
// by its codec even though the ledger stores, snapshots and replays it.

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// zzqaStamped is a message that carries a time, the way a request's timestamp
// reaches a decider: the shell reads the clock and puts it in the message, so
// Decide stays deterministic and Verify can replay it.
type zzqaStamped struct{ At time.Time }

type zzqaStampedMsg interface{ isZZQAStamped() }

func (zzqaStamped) isZZQAStamped() {}

// zzqaStampState is the state that remembers it.
type zzqaStampState struct{ At time.Time }

func zzqaCodecs() *codec.Set[zzqaStampedMsg, int, zzqaStamped, struct{}] {
	msgs := codec.New[zzqaStampedMsg]()
	codec.Register[zzqaStamped](msgs, "zzqa.stamp.msg", 1)
	events := codec.New[zzqaStamped]()
	codec.Register[zzqaStamped](events, "zzqa.stamp.event", 1)
	results := codec.New[int]()
	codec.Register[int](results, "zzqa.stamp.result", 1)
	return &codec.Set[zzqaStampedMsg, int, zzqaStamped, struct{}]{
		Messages: msgs, Results: results, Events: events, Effects: codec.New[struct{}](),
	}
}

// zzqaCarriedAsTheSameInstant checks the fixture against the ledger's own rule:
// codec.CheckStorable accepts a time whose JSON round trip is the same instant,
// and queueSnapshot refuses only a value that is not. Asserting it here keeps
// the test from passing for the wrong reason — a time JSON actually moves is a
// different defect, refused before anything is stored.
func zzqaCarriedAsTheSameInstant(t *testing.T, at time.Time) {
	t.Helper()
	if err := codec.CheckStorable(zzqaStamped{At: at}); err != nil {
		t.Fatalf("%v is refused before storage, so it is not the case under test: %v", at, err)
	}
	data, err := at.MarshalJSON()
	if err != nil {
		t.Fatalf("%v cannot be encoded: %v", at, err)
	}
	var back time.Time
	if err := back.UnmarshalJSON(data); err != nil {
		t.Fatalf("%s cannot be decoded: %v", data, err)
	}
	if !back.Equal(at) {
		t.Fatalf("%v is stored as %v, a different instant, so it is not the case under test", at, back)
	}
}

// D6 says a message survives its codec. A timestamp the shell read from the
// clock does: the ledger records it, and Verify replays it as the same instant.
// The checker must not fail the machine over the monotonic reading JSON drops.
func TestATimeTheCodecCarriesUnchangedPassesTheSpec(t *testing.T) {
	zzqaCarriedAsTheSameInstant(t, time.Now())
	m := spectest.Machine[zzqaStampState, zzqaStampedMsg, int, zzqaStamped, struct{}]{
		Decider: cairn.Decider[zzqaStampState, zzqaStampedMsg, int, zzqaStamped, struct{}]{
			Initial: func() zzqaStampState { return zzqaStampState{} },
			Decide: func(s zzqaStampState, msg zzqaStampedMsg) cairn.Decision[int, zzqaStamped, struct{}] {
				return cairn.Accept[int, zzqaStamped, struct{}](0, []zzqaStamped{msg.(zzqaStamped)})
			},
			Evolve: func(s zzqaStampState, e zzqaStamped) zzqaStampState {
				s.At = e.At
				return s
			},
		},
		Messages: func(t *rapid.T, _ zzqaStampState) zzqaStampedMsg {
			return zzqaStamped{At: time.Now()}
		},
		Codecs: zzqaCodecs(),
	}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("a message carrying the current time is stored as the same instant, so the check must pass: err=%v failed=%v\n%s",
			err, failed, msg)
	}
}

// The same for a time in a named zone: time.Time.MarshalJSON writes the offset,
// so the instant is kept, but decoding builds a new *Location, so DeepEqual
// calls the state changed. A snapshot keeps the instant, which is what a fold
// after the snapshot sees, so D7 must not fail it.
func TestATimeTheLedgerSnapshotsUnchangedPassesTheSpec(t *testing.T) {
	at := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*60*60+30*60))
	zzqaCarriedAsTheSameInstant(t, at)
	m := spectest.Machine[zzqaStampState, int, int, int, struct{}]{
		Decider: cairn.Decider[zzqaStampState, int, int, int, struct{}]{
			Initial: func() zzqaStampState { return zzqaStampState{} },
			Decide: func(s zzqaStampState, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](msg, []int{msg})
			},
			Evolve: func(s zzqaStampState, _ int) zzqaStampState {
				s.At = at
				return s
			},
		},
		Messages:  func(t *rapid.T, _ zzqaStampState) int { return 0 },
		Snapshots: true,
	}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("a state carrying a time whose instant survives the snapshot is what the ledger stores, so the check must pass: err=%v failed=%v\n%s",
			err, failed, msg)
	}
}

// zzqaDropped drops a field in its encoding, the way a `json:"-"` tag does.
type zzqaDropped struct {
	At   time.Time
	Memo string `json:"-"`
}

type zzqaLossy interface{ isZZQALossy() }

func (zzqaDropped) isZZQALossy() {}

// The check's teeth: a field the codec drops is still a violation of D6. A fix
// for the time comparison must not turn the round trip into a comparison that
// accepts data loss.
func TestAMessageFieldTheCodecDropsStillFailsD6(t *testing.T) {
	droppedMessages := codec.New[zzqaLossy]()
	codec.Register[zzqaDropped](droppedMessages, "zzqa.lossy", 1)

	m := spectest.Machine[zzqaStampState, zzqaLossy, int, struct{}, struct{}]{
		Decider: cairn.Decider[zzqaStampState, zzqaLossy, int, struct{}, struct{}]{
			Initial: func() zzqaStampState { return zzqaStampState{} },
			Decide: func(zzqaStampState, zzqaLossy) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](0, nil)
			},
			Evolve: func(s zzqaStampState, _ struct{}) zzqaStampState { return s },
		},
		Messages: func(t *rapid.T, _ zzqaStampState) zzqaLossy {
			return zzqaDropped{At: time.Now().UTC(), Memo: "rent"}
		},
		Codecs: &codec.Set[zzqaLossy, int, struct{}, struct{}]{Messages: droppedMessages},
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Encodable.ID+" violated") {
		t.Fatalf("a message field the codec drops must still fail %s, got err=%v failed=%v:\n%s",
			spectest.Encodable.ID, err, failed, msg)
	}
}

// The check's teeth, on the other side of the same comparison: a zone offset
// with seconds in it is written only to the minute, so JSON stores a different
// instant with no error at all. Accepting a time whose instant is kept must not
// accept one that moved.
func TestASnapshotThatMovesAnInstantStillFailsD7(t *testing.T) {
	at := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("LMT", -(5*60*60+53*60+7)))
	if err := codec.CheckStorable(zzqaStampState{At: at}); err == nil {
		t.Fatalf("the ledger stores %v unchanged, so it is not the case under test", at)
	}
	m := spectest.Machine[zzqaStampState, int, int, int, struct{}]{
		Decider: cairn.Decider[zzqaStampState, int, int, int, struct{}]{
			Initial: func() zzqaStampState { return zzqaStampState{} },
			Decide: func(s zzqaStampState, msg int) cairn.Decision[int, int, struct{}] {
				return cairn.Accept[int, int, struct{}](msg, []int{msg})
			},
			Evolve: func(s zzqaStampState, _ int) zzqaStampState {
				s.At = at
				return s
			},
		},
		Messages:  func(t *rapid.T, _ zzqaStampState) int { return 0 },
		Snapshots: true,
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Snapshottable.ID+" violated") {
		t.Fatalf("a snapshot that stores a different instant must fail %s, got err=%v failed=%v:\n%s",
			spectest.Snapshottable.ID, err, failed, msg)
	}
}
