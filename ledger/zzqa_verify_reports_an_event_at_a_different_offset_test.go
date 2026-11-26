package ledger_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// zzqaEndOfDayIn settles a message's day in the register's zone and writes the
// deadline in another one. The instant is the same, but RFC 3339 stores a
// different offset, so the two events do not encode to the same bytes.
func zzqaEndOfDayIn(zone *time.Location) cairn.Decider[zzqaDay, zzqaDayMsg, int, zzqaDayEvent, struct{}] {
	return cairn.Decider[zzqaDay, zzqaDayMsg, int, zzqaDayEvent, struct{}]{
		Initial: func() zzqaDay { return zzqaDay{} },
		Decide: func(_ zzqaDay, m zzqaDayMsg) cairn.Decision[int, zzqaDayEvent, struct{}] {
			o := m.(zzqaOpen)
			y, mo, d := o.At.In(zzqaZone).Date()
			end := time.Date(y, mo, d, 23, 59, 59, 0, zzqaZone)
			return cairn.Accept[int, zzqaDayEvent, struct{}](0, []zzqaDayEvent{zzqaOpened{At: end.In(zone)}})
		},
		Evolve: func(_ zzqaDay, e zzqaDayEvent) zzqaDay {
			return zzqaDay{Deadline: e.(zzqaOpened).At}
		},
	}
}

// Verify asks whether today's decider would decide the same. Two events at the
// same instant but a different zone offset are not the same event: RFC 3339
// stores the offset, so the bytes differ. Verify must report that, and the
// unchanged-time test beside this one must keep reporting nothing.
func TestZZQAVerifyReportsAnEventAtADifferentOffset(t *testing.T) {
	t.Parallel()
	agg := zzqaDayAggregate(t)

	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	h, err := agg.Handle(ctx, "d1", zzqaOpen{At: at})
	if err != nil {
		t.Fatal(err)
	}

	later := *agg
	later.Decider = zzqaEndOfDayIn(time.FixedZone("EET", 2*3600))

	// The premise: the two deciders emit the same instant, but at offsets RFC
	// 3339 writes differently, so this is a changed stored value and not an
	// unchanged one Verify should stay quiet about.
	msg, err := agg.Codecs.Messages.Decode(h.Entry.Message)
	if err != nil {
		t.Fatal(err)
	}
	d := later.Decider.Decide(later.Decider.Initial(), msg)
	if d.Refusal != nil || len(d.Events) != 1 {
		t.Fatalf("re-deciding the stored message: %+v", d)
	}
	fresh, err := later.Codecs.Events.Encode(d.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	stored, err := later.Codecs.Events.Decode(h.Entry.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	storedAt, freshAt := stored.(zzqaOpened).At, d.Events[0].(zzqaOpened).At
	_, storedOffset := storedAt.Zone()
	_, freshOffset := freshAt.Zone()
	if !storedAt.Equal(freshAt) || storedOffset == freshOffset {
		t.Fatalf("the premise does not hold: stored %v (%d), replay %v (%d)",
			storedAt, storedOffset, freshAt, freshOffset)
	}
	if bytes.Equal(fresh.Data, h.Entry.Events[0].Data) {
		t.Fatal("the premise does not hold: the two offsets encode to the same bytes")
	}

	div, err := later.Verify(ctx, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(div) != 1 || div[0].Aspect != ledger.Emitted {
		t.Fatalf("Verify reports %v, want one Emitted divergence for the changed offset", div)
	}
}
