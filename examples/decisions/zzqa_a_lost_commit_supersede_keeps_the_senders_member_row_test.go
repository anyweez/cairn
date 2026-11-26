package decisions

// The register keeps the names behind its member ids in its own table, and
// resolves the sender before every request. Supersede is the one Service
// method that composes a Store.Transact instead of calling Handle, because it
// locks both decisions. When that transaction's COMMIT response is lost the
// ledger holds the Supersede entry, but Transact reports the failure as it
// receives it — wrapping neither ErrRecorded nor ErrUnknownOutcome — so
// mayHaveRecorded reads it as "nothing was recorded" and the deferred
// Content.done releases the sender's member row.
//
// The entry the ledger recorded is attributed to that member id, so the
// register can no longer say who superseded the decision, and the same
// person's next request mints a second id: one person, two identities in an
// append-only history. The test proves the supersede was recorded before it
// checks the row exists.

import (
	"context"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

func TestALostCommitSupersedeKeepsTheSendersMemberRow(t *testing.T) {
	ctx := context.Background()
	verify := ledgertest.NewStore(t, ledger.RequireActor(false))
	setup := NewService(verify)

	// Two decided decisions, so one can supersede the other.
	oldID, err := setup.Create(ctx, "ana", "ship the beta?", "yes")
	must(t, err)
	oldState, err := setup.Get(ctx, oldID)
	must(t, err)
	must(t, setup.RecordOutcome(ctx, oldID, "ana", oldState.Options[0].ID, "the bugs are cosmetic"))

	newID, err := setup.Create(ctx, "ben", "ship the beta later?", "yes")
	must(t, err)
	newState, err := setup.Get(ctx, newID)
	must(t, err)
	must(t, setup.RecordOutcome(ctx, newID, "ben", newState.Options[0].ID, "the bugs are cosmetic"))

	// The Supersede runs over connections that never deliver the COMMIT's
	// CommandComplete: the entry is recorded, and the caller is told it failed.
	lossy := commitLostRegister(t, verify)
	if err := lossy.Supersede(ctx, oldID, newID, "newcomer"); err == nil {
		t.Fatal("Supersede returned no error, so the connection did not fail at the commit")
	}

	// The premise: the supersede was recorded, and its entry is attributed to
	// the member id the lossy service minted for "newcomer".
	superseded, err := setup.Get(ctx, oldID)
	must(t, err)
	if superseded.Status != Superseded {
		t.Fatalf("premise: the supersede was not recorded (status %v), so no member row was needed", superseded.Status)
	}
	entries, err := verify.ReadStream(ctx, setup.Register.Stream(oldID), 0)
	must(t, err)
	if len(entries) == 0 {
		t.Fatal("premise: the stream holds no entries")
	}
	last := entries[len(entries)-1]
	if last.Meta.Actor == "" {
		t.Fatalf("premise: the recorded supersede %s#%d names no actor", last.Stream, last.Seq)
	}
	sender := Member(last.Meta.Actor)

	// The recorded entry names this member id, so the register must still
	// resolve it to the name the request used.
	if got := lossy.Content.Name(sender); got != "newcomer" {
		t.Fatalf("the recorded supersede is attributed to member %q, but the register's table resolves it to %q: "+
			"the sender's row was released for a request the ledger recorded", sender, got)
	}

	// And the same person's next request must keep the one identity the
	// ledger's history already uses.
	if again := lossy.Content.Member("newcomer"); again != sender {
		t.Fatalf("a later request by the same person minted member %q, not the recorded %q: one person now has two ids "+
			"in an append-only history", again, sender)
	}
}
