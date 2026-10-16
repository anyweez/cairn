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

// The register's own established rule DR2, SettledCarriesOutcome, says a
// decided decision carries a complete outcome: Chosen, Rationale, DecidedBy
// and a non-zero DecidedAt. RecordOutcome validates Chosen and Rationale but
// not By or At, so an incomplete outcome is accepted and stored, and the
// resulting state violates DR2. The spec generator never draws an empty actor
// or a zero time, so the checker does not see it.

// The root cause, no database: Decide accepts an outcome with no actor or no
// time.
func TestRecordOutcomeRequiresActorAndTime(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	base := func(t *testing.T) State {
		t.Helper()
		state := Decider.Initial()
		d, state := Decider.Step(state, Create{ID: "d-1", By: "ana", At: at, Title: "ship it?",
			Options: []Option{{ID: "o1", Title: "yes"}}})
		if d.Refused() {
			t.Fatal(d.Refusal)
		}
		return state
	}

	t.Run("no actor", func(t *testing.T) {
		d, state := Decider.Step(base(t), RecordOutcome{By: "", At: at, Chosen: "o1", Rationale: "because"})
		if !d.Refused() {
			t.Fatalf("accepted an outcome with no DecidedBy: %+v", state.Outcome)
		}
	})
	t.Run("no time", func(t *testing.T) {
		d, state := Decider.Step(base(t), RecordOutcome{By: "ana", At: time.Time{}, Chosen: "o1", Rationale: "because"})
		if !d.Refused() {
			t.Fatalf("accepted an outcome with a zero DecidedAt: %+v", state.Outcome)
		}
	})
}

// The user-facing symptom: a zero At on a RecordOutcome reaches the ledger and
// is stored as a settled decision whose outcome is timestamped year 1, even
// though DR2 forbids it. Needs CAIRN_TEST_DATABASE_URL.
func TestRecordOutcomeZeroTimeReachesTheLedger(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	id, err := svc.Create(ctx, "ana", "ship it?", "yes")
	if err != nil {
		t.Fatal(err)
	}
	state, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// Go through Handle directly, with a zero At, as the public message API
	// allows. The service's own RecordOutcome always stamps the time.
	_, err = svc.Register.Handle(ctx, id, RecordOutcome{By: "ana", At: time.Time{}, Chosen: state.Options[0].ID, Rationale: "because"})
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a zero At should be refused by the decider, got %v", err)
	}
}
