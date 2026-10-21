package decisions

import (
	"context"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A message's time is part of the decision: RecordOutcome stores it as the
// outcome's DecidedAt and Create stores it on the Created event. decide only
// refuses the zero time, and codec.CheckStorable skips a time.Time because it
// implements json.Marshaler, so a time the JSON codec cannot carry faithfully
// is accepted.
//
// time.Time.MarshalJSON writes an RFC 3339 offset with minute precision. A
// zone whose UTC offset is not a whole number of minutes — every tzdata zone
// before about 1900 has one, e.g. Europe/Amsterdam was +00:19:32 — loses the
// seconds part of its offset, which moves the instant. json.Marshal reports no
// error, so the ledger stores a time the caller never sent and, entries being
// append-only, can never correct it.
func TestARecordedOutcomeKeepsTheTimeItWasSent(t *testing.T) {
	// +00:19:32, the offset Europe/Amsterdam had in 1900. The zone is built
	// directly so the test does not depend on the host's tzdata.
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))

	state := Decider.Initial()
	var d decision
	if d, state = Decider.Step(state, Create{
		ID: "d-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(), Title: "ship it?",
		Options: []Option{{ID: "o1", Title: "yes"}},
	}); d.Refused() {
		t.Fatalf("Create refused: %v", d.Refusal)
	}
	var recorded decision
	if recorded, state = Decider.Step(state, RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "because"}); recorded.Refused() {
		t.Fatalf("RecordOutcome refused the time instead of accepting it: %v", recorded.Refusal)
	}

	// What the ledger keeps is the event through its codec.
	set := Codecs()
	envelopes, err := set.Events.EncodeAll(recorded.Events)
	if err != nil {
		t.Fatalf("encoding the event: %v", err)
	}
	events, err := set.Events.DecodeAll(envelopes)
	if err != nil {
		t.Fatalf("decoding the event: %v", err)
	}
	stored := events[0].(DecisionRecorded).Outcome.DecidedAt

	if !stored.Equal(at) {
		t.Fatalf("the stored outcome is decided at %v (%s), %v after the time the caller sent (%s); "+
			"the offset lost its 32 seconds, so the ledger holds a different instant",
			stored, stored.Format(time.RFC3339), stored.Sub(at), at.Format(time.RFC3339))
	}
}

// The same value through the shell and a ledger: the shell's clock is in a
// zone JSON cannot write exactly, and the entry the ledger records keeps the
// instant it stamped. Replaying the stream decides the same time again.
func TestTheLedgerKeepsTheInstantAZoneOffsetWouldMove(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	svc.Now = func() time.Time { return at }

	id, err := svc.Create(ctx, "ana", "ship it?", "yes")
	must(t, err)
	entries, err := svc.Register.Store.ReadStream(ctx, svc.Register.Stream(id), 0)
	must(t, err)
	events, err := Codecs().Events.DecodeAll(entries[0].Events)
	must(t, err)
	if created := events[0].(Created); !created.At.Equal(at) {
		t.Fatalf("the creation is recorded at %v, %v after the time the shell stamped (%v)",
			created.At, created.At.Sub(at), at)
	}
	divergences, err := svc.Register.Verify(ctx, id)
	must(t, err)
	if len(divergences) != 0 {
		t.Fatalf("replaying the normalised time diverged: %+v", divergences)
	}
}
