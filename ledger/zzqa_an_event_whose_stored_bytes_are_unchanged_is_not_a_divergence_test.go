package ledger_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A decider that settles a message's day from the time the message carries: the
// end of that day in the zone the register works in. Nothing reads the clock, so
// the decision is deterministic and replayable, and the event it stores is
// exactly the time a replay of the message produces.

// zzqaZone is the zone the register settles a day in. Its name is part of the
// Go value, but not of the JSON the ledger keeps: RFC 3339 carries the offset.
var zzqaZone = time.FixedZone("CET", 3600)

type zzqaDay struct{ Deadline time.Time }

type zzqaDayMsg interface{ isZZQADayMsg() }

type zzqaOpen struct{ At time.Time }

func (zzqaOpen) isZZQADayMsg() {}

type zzqaDayEvent interface{ isZZQADayEvent() }

type zzqaOpened struct{ At time.Time }

func (zzqaOpened) isZZQADayEvent() {}

func zzqaEndOfDay(_ zzqaDay, m zzqaDayMsg) cairn.Decision[int, zzqaDayEvent, struct{}] {
	o := m.(zzqaOpen)
	y, mo, d := o.At.In(zzqaZone).Date()
	end := time.Date(y, mo, d, 23, 59, 59, 0, zzqaZone)
	return cairn.Accept[int, zzqaDayEvent, struct{}](0, []zzqaDayEvent{zzqaOpened{At: end}})
}

func zzqaDayAggregate(t *testing.T) *ledger.Aggregate[zzqaDay, zzqaDayMsg, int, zzqaDayEvent, struct{}] {
	t.Helper()
	msgs, events := codec.New[zzqaDayMsg](), codec.New[zzqaDayEvent]()
	codec.Register[zzqaOpen](msgs, "open", 1)
	codec.Register[zzqaOpened](events, "opened", 1)
	results, effects := codec.New[int](), codec.New[struct{}]()
	codec.Register[int](results, "zero", 1)
	return &ledger.Aggregate[zzqaDay, zzqaDayMsg, int, zzqaDayEvent, struct{}]{
		Store: ledgertest.NewStore(t), Kind: "day",
		Decider: cairn.Decider[zzqaDay, zzqaDayMsg, int, zzqaDayEvent, struct{}]{
			Initial: func() zzqaDay { return zzqaDay{} },
			Decide:  zzqaEndOfDay,
			Evolve: func(_ zzqaDay, e zzqaDayEvent) zzqaDay {
				return zzqaDay{Deadline: e.(zzqaOpened).At}
			},
		},
		Codecs: &codec.Set[zzqaDayMsg, int, zzqaDayEvent, struct{}]{
			Messages: msgs, Results: results, Events: events, Effects: effects,
		},
	}
}

// Verify re-decides each stored message and reports where the current decider
// disagrees with what was recorded. The event it re-decides here encodes to the
// same bytes that are stored, so nothing was lost and there is nothing to
// report; a comparison stricter than the bytes the ledger keeps (Go value
// identity of a time.Time built from the message) must not invent one.
func TestZZQAAnEventWhoseStoredBytesAreUnchangedIsNotADivergence(t *testing.T) {
	t.Parallel()
	agg := zzqaDayAggregate(t)

	// A time in a zone that is not UTC: JSON keeps the instant and the offset,
	// which is what the ledger stores and reads back.
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	h, err := agg.Handle(ctx, "d1", zzqaOpen{At: at})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Entry.Events) != 1 {
		t.Fatalf("the message recorded %d events", len(h.Entry.Events))
	}

	// The premise: replaying the stored message decides the same event, and its
	// encoding is byte for byte what is in the entry. So a divergence here
	// could not be a change in what the decider says.
	msg, err := agg.Codecs.Messages.Decode(h.Entry.Message)
	if err != nil {
		t.Fatal(err)
	}
	d := agg.Decider.Decide(agg.Decider.Initial(), msg)
	if d.Refusal != nil || len(d.Events) != 1 {
		t.Fatalf("re-deciding the stored message: %+v", d)
	}
	fresh, err := agg.Codecs.Events.Encode(d.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fresh.Data, h.Entry.Events[0].Data) {
		t.Fatalf("the premise does not hold: the stored event is %s, a replay decides %s",
			h.Entry.Events[0].Data, fresh.Data)
	}
	storedEvent, err := agg.Codecs.Events.Decode(h.Entry.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	if !storedEvent.(zzqaOpened).At.Equal(d.Events[0].(zzqaOpened).At) {
		t.Fatalf("the stored deadline is %v, not the %v a replay decides",
			storedEvent.(zzqaOpened).At, d.Events[0].(zzqaOpened).At)
	}

	div, err := agg.Verify(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(div) != 0 {
		t.Fatalf("Verify reports a divergence for an entry whose stored event encodes to the same bytes: %v", div)
	}
}
