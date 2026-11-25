package decisions

import (
	"context"
	"errors"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The register keeps the words and the names in its own table (Content), so a
// request that records nothing must leave no row there. The shell says exactly
// that: forgetUnlessRecorded's comment in service.go —
//
//	Every other error — a refusal, a guard, a validation failure, a
//	transaction the database rolled back — means no entry names them, so the
//	cleanup runs.
//
// — and service_recorded_text_test.go's TestACreateAGuardRefusesLeavesNoTextBehind
// hold the text rows to it. The sender's row is not cleaned: Service calls
// Content.Member(by) before Handle, so a name minted for a request the ledger
// never recorded stays in the table, with no entry and no stream that names
// its id. The documented erasure walks the rows a decision's state names and
// then Forget()s the member, so it cannot reach this one: nothing points at it.
//
// This is not the case t3451.2 dismissed. There a refused message was recorded
// — the refusal is an entry, attributed to the sender's member id — so the row
// is what makes that recorded actor resolvable. Here the ledger records
// nothing at all, which is the premise that note's probe got wrong and these
// subtests assert first.
func TestARequestThatRecordsNothingLeavesNoMemberRow(t *testing.T) {
	ctx := context.Background()

	// The table's rows, counted under its own lock because Member maps a name
	// to an id on first sight and would mint the row it is being asked about.
	rows := func(svc *Service) (text, names, ids int) {
		svc.Content.mu.Lock()
		defer svc.Content.mu.Unlock()
		return len(svc.Content.text), len(svc.Content.names), len(svc.Content.ids)
	}
	// The premise every subtest shares: the ledger recorded nothing, so no
	// entry's actor names the sender and nothing may keep their row.
	recordedNothing := func(t *testing.T, svc *Service) {
		t.Helper()
		streams, err := svc.Register.Store.Streams(ctx, "decision")
		if err != nil {
			t.Fatal(err)
		}
		if len(streams) != 0 {
			t.Fatalf("the ledger recorded %v, so this test is not about a request that recorded nothing", streams)
		}
	}
	noRows := func(t *testing.T, svc *Service) {
		t.Helper()
		recordedNothing(t, svc)
		text, names, ids := rows(svc)
		if text != 0 {
			t.Errorf("the refused request left %d rows of text behind", text)
		}
		if names != 0 || ids != 0 {
			t.Errorf("the refused request left %d name and %d id rows behind, so the register's table names a person no entry and no decision names",
				names, ids)
		}
	}

	t.Run("a stream id the ledger refuses", func(t *testing.T) {
		svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
		// A caller that lost the decision's id, and a sender it has not seen.
		if err := svc.SavePosition(ctx, "", "ana.sorensen@example.com", "o1", "she can support it"); err == nil {
			t.Fatal("a position on the empty stream id was accepted")
		}
		noRows(t, svc)
	})

	t.Run("a guard that refuses before the ledger is sent to", func(t *testing.T) {
		svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
		// The message names its sender, so metadata naming somebody else is
		// refused before the message reaches the ledger.
		mismatched := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "ben"})
		_, err := svc.Create(mismatched, "ana.sorensen@example.com", "Ship the beta?", "yes")
		if !errors.Is(err, ledger.ErrActorMismatch) {
			t.Fatalf("Create returned %v, want ErrActorMismatch", err)
		}
		noRows(t, svc)
	})
}

// A row the ledger does need must survive: a member a recorded entry names
// still resolves to their name, and a failed request must not forget a member
// whose id its entries carry.
func TestARecordedMembersRowSurvivesARefusedRequest(t *testing.T) {
	ctx := context.Background()

	// A refusal is recorded, and its entry's actor is the sender's member id.
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
	id, err := svc.Create(ctx, "ana", "Ship the beta?", "yes")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	must(t, svc.SavePosition(ctx, id, "ben", state.Options[0].ID, "it is ready"))
	must(t, svc.RecordOutcome(ctx, id, "ana", state.Options[0].ID, "the bugs are cosmetic"))

	// Refused because a decided decision's positions are frozen, and recorded:
	// cy's id is the actor of the refusal entry.
	if err := svc.SavePosition(ctx, id, "cy", state.Options[0].ID, "too late"); err == nil {
		t.Fatal("a position on a decided decision was accepted")
	}
	if name := svc.Content.Name(svc.Content.Member("cy")); name != "cy" {
		t.Fatalf("the recorded refusal's actor no longer resolves: member cy reads as %q", name)
	}

	// A failed request from a member the ledger already knows must not forget
	// them either.
	if err := svc.SavePosition(ctx, "", "ana", "o1", "again"); err == nil {
		t.Fatal("a position on the empty stream id was accepted")
	}
	if name := svc.Content.Name(svc.Content.Member("ana")); name != "ana" {
		t.Fatalf("a failed request forgot a member its entries name: ana reads as %q", name)
	}
}
