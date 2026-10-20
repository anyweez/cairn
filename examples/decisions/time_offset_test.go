package decisions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A time whose zone offset is not a whole number of minutes is accepted by
// Decide, encoded without error, and stored as a different instant: RFC 3339
// carries an offset only to the minute, so time.Time.MarshalJSON truncates the
// seconds part. Entries are append-only, so the wrong instant is permanent.
// tzdata reaches this with historical offsets (Europe/Amsterdam before 1937 is
// +00:19:32, Europe/Paris +00:09:21, Africa/Monrovia -00:43:08), and a caller
// can build one directly with time.FixedZone.
//
// The decider already proves a message survives its codec (README D6) by
// running codec.CheckStorable, which used to trust time.Time because it
// implements json.Marshaler. CheckStorable now requires the value to come back
// Equal from its own JSON, so the message is refused rather than recorded as
// an instant the caller never sent.

func secondOffsetTime() time.Time {
	return time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
}

// The root cause, no database: Decide accepts a time the codec cannot keep.
func TestASecondOffsetTimeIsRefusedByDecide(t *testing.T) {
	at := secondOffsetTime()
	create, _ := Decider.Step(State{}, Create{ID: "d-1", By: "ana", At: at, Title: "ship?",
		Options: []Option{{ID: "o1", Title: "yes"}}})
	if !create.Refused() {
		t.Fatalf("Decide accepted a Create whose time cannot survive its codec: %+v", create)
	}

	state := Decider.Initial()
	d, next := Decider.Step(state, Create{ID: "d-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(),
		Title: "ship?", Options: []Option{{ID: "o1", Title: "yes"}}})
	if d.Refused() {
		t.Fatal(d.Refusal)
	}
	outcome, _ := Decider.Step(next, RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "because"})
	if !outcome.Refused() {
		t.Fatalf("Decide accepted a RecordOutcome whose time cannot survive its codec: %+v", outcome)
	}
}

// The user-facing symptom, through the public message API and a real ledger:
// the stored outcome must be decided at the instant the caller sent, or the
// message must be refused. It must never be recorded at a different instant.
// Skipped when no test database is configured.
func TestARecordedOutcomeKeepsTheTimeItWasSent(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	id, err := svc.Create(ctx, "ana", "Ship it?", "yes")
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	at := secondOffsetTime()
	_, err = svc.Register.Handle(ctx, id, RecordOutcome{
		By: "ana", At: at, Chosen: before.Options[0].ID, Rationale: "because",
	})
	var refusal *cairn.Refusal
	if errors.As(err, &refusal) {
		return // refused, so the ledger holds no wrong instant
	}
	if err != nil {
		t.Fatalf("recording an outcome at %v: %v", at, err)
	}

	state, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if state.Outcome == nil {
		t.Fatal("the outcome was accepted but not stored")
	}
	if !state.Outcome.DecidedAt.Equal(at) {
		t.Fatalf("the stored outcome is decided at %v, %v after the time the caller sent (%v); the offset lost its 32 seconds, so the ledger holds a different instant",
			state.Outcome.DecidedAt, state.Outcome.DecidedAt.Sub(at), at)
	}
}
