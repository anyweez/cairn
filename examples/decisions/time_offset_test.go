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

// A message's time is part of the decision: RecordOutcome stores it as the
// outcome's DecidedAt and Create stores it on the Created event. time.Time's
// JSON encoding carries an RFC 3339 offset only to the minute, so a zone whose
// UTC offset is not a whole number of minutes — every tzdata zone before about
// 1900 has one, e.g. Europe/Amsterdam was +00:19:32 — would be stored as a
// different instant with no error at all.
//
// The ledger encodes the message the caller sent, not the decider's copy of
// it, so the decider has to refuse that value: rewriting its own copy would
// still leave the codec encoding the offset it cannot carry.
func TestARecordedOutcomeWhoseZoneOffsetHasSecondsIsRefused(t *testing.T) {
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
	if recorded, state = Decider.Step(state, RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "because"}); !recorded.Refused() {
		t.Fatalf("Decide accepted a RecordOutcome whose time cannot be encoded faithfully: %+v", recorded.Result)
	}
	if recorded.Refusal.Code != Invalid {
		t.Fatalf("refusal code %q, want %q", recorded.Refusal.Code, Invalid)
	}
}

// The same value through the shell and a ledger: the caller gets the
// register's refusal rather than a codec error raised after Decide said yes,
// and the refused message leaves no entry behind.
func TestARecordOutcomeInAZoneJSONCannotWriteIsRefusedThroughTheLedger(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
	stream := svc.Register.Stream("d-1")
	if _, err := svc.Register.Handle(ctx, "d-1", Create{
		ID: "d-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(), Title: "ship it?",
		Options: []Option{{ID: "o1", Title: "yes"}},
	}); err != nil {
		t.Fatal(err)
	}

	at := time.Date(1900, time.January, 1, 12, 0, 0, 0, time.FixedZone("AMT", 19*60+32))
	_, err := svc.Register.Handle(ctx, "d-1", RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "because"})
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) || refusal.Code != Invalid {
		t.Fatalf("want the register's refusal, got %v", err)
	}
	entries, err := svc.Register.Store.ReadStream(ctx, stream, 0)
	must(t, err)
	if len(entries) != 1 {
		t.Fatalf("the refused outcome left %d entries behind, want only the Create", len(entries))
	}
}
