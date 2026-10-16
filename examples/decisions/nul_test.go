package decisions

import (
	"context"
	"errors"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The same root as text_fidelity_test.go: message text is accepted by the
// decider without checking it can be stored. A NUL byte is valid UTF-8 but
// jsonb cannot hold it, so the decider accepts a title that the ledger then
// rejects with a raw Postgres error, "unsupported Unicode escape sequence
// (SQLSTATE 22P05)".

// The root cause, no database: Decide accepts a title containing a NUL byte
// instead of refusing it.
func TestNULTitleIsRefusedByDecide(t *testing.T) {
	s := Decider.Initial()
	d, _ := Decider.Step(s, Create{ID: "d-1", By: "ana", Title: "ship\x00it"})
	if !d.Refused() {
		t.Fatalf("Decide accepted a title containing a NUL byte: %+v", d)
	}
}

// The user-facing symptom: the service returns a driver error rather than a
// refusal. Skipped when no test database is configured.
func TestNULTitleIsRefusedNotFatal(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	_, err := svc.Create(ctx, "ana", "ship\x00it", "yes")
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a title with a NUL byte should be refused by the decider, got %v", err)
	}
}
