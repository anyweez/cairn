package spectest_test

// A time.Time is stored by its instant, so a map keyed by one survives a codec
// or snapshot round trip even though decoding gives the key a new *Location.
// The checker must judge the key the way codec.CheckStorable does, or a machine
// whose state or message is keyed by a time cannot pass its spec.

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// zzqaKeyAt is storable: its zone offset is a whole number of minutes, so JSON
// keeps the instant, even though the decoded key's *Location is a new one.
var zzqaKeyAt = time.Date(2026, time.March, 4, 5, 6, 7, 0, time.FixedZone("IST", 5*60*60+30*60))

type zzqaKeyedState struct{ M map[time.Time]int }

type zzqaKeyedMsg struct{ M map[time.Time]int }

func zzqaKeyedMachinery[S any](initial S) spectest.Machine[S, zzqaKeyedMsg, int, struct{}, struct{}] {
	return spectest.Machine[S, zzqaKeyedMsg, int, struct{}, struct{}]{
		Decider: cairn.Decider[S, zzqaKeyedMsg, int, struct{}, struct{}]{
			Initial: func() S { return initial },
			Decide: func(S, zzqaKeyedMsg) cairn.Decision[int, struct{}, struct{}] {
				return cairn.Accept[int, struct{}, struct{}](0, nil)
			},
			Evolve: func(s S, _ struct{}) S { return s },
		},
		Messages: func(_ *rapid.T, _ S) zzqaKeyedMsg {
			return zzqaKeyedMsg{M: map[time.Time]int{zzqaKeyAt: 1}}
		},
		Steps: 1,
	}
}

func TestQATimeMapKeySnapshotRoundTripPassesTheSpec(t *testing.T) {
	// The premise: this is a state the ledger can snapshot.
	if err := codec.CheckStorable(zzqaKeyedState{M: map[time.Time]int{zzqaKeyAt: 1}}); err != nil {
		t.Fatalf("the state is not storable, so it is not the case under test: %v", err)
	}
	m := zzqaKeyedMachinery(zzqaKeyedState{M: map[time.Time]int{zzqaKeyAt: 1}})
	m.Snapshots = true
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("a state keyed by a time the snapshot keeps unchanged must pass: err=%v failed=%v\n%s", err, failed, msg)
	}
}

func TestQATimeMapKeyCodecRoundTripPassesTheSpec(t *testing.T) {
	msg := zzqaKeyedMsg{M: map[time.Time]int{zzqaKeyAt: 1}}
	if err := codec.CheckStorable(msg); err != nil {
		t.Fatalf("the message is not storable, so it is not the case under test: %v", err)
	}
	msgs := codec.New[zzqaKeyedMsg]()
	codec.Register[zzqaKeyedMsg](msgs, "qa.keyed", 1)
	results := codec.New[int]()
	codec.Register[int](results, "qa.result", 1)
	events := codec.New[struct{}]()
	codec.Register[struct{}](events, "qa.event", 1)
	effects := codec.New[struct{}]()
	codec.Register[struct{}](effects, "qa.effect", 1)
	m := zzqaKeyedMachinery(struct{}{})
	m.Codecs = &codec.Set[zzqaKeyedMsg, int, struct{}, struct{}]{
		Messages: msgs, Results: results, Events: events, Effects: effects,
	}
	if msg, failed, err := spectest.Fails(t, m.Check); err != nil || failed {
		t.Fatalf("a message keyed by a time the codec keeps unchanged must pass: err=%v failed=%v\n%s", err, failed, msg)
	}
}
