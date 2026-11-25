package decisions

import (
	"context"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// content_test.go documents the register's erasure procedure: delete the rows
// the decision's state names — the question, each option's title, the member's
// position body, the rationale — then forget the name behind the member id.
// docs/REDACTION.md promises that this takes the words out of the application's
// table, which is the only place they live.
//
// A member may file a position more than once while a decision is open: the
// second SavePosition replaces the first in the state. The first body is then
// no longer named by the state, so the documented procedure never deletes its
// row, and the words a deletion request was meant to erase stay in the table.
// The ledger still names the old ref in the entry that recorded it.
func TestErasureRemovesEveryPositionBodyTheMemberWrote(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	const (
		member      = "ana.sorensen@example.com"
		oldBody     = "Ana's team cannot support it before the holidays"
		currentBody = "Ana's team can support it after the holidays"
	)

	id, err := svc.Create(ctx, member, "Should we ship the beta to Ana's team?", "ship it")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	option := state.Options[0].ID

	// The member files a position, then changes it. Only the second is in the
	// state when the erasure request arrives.
	must(t, svc.SavePosition(ctx, id, member, option, oldBody))
	state, err = svc.Get(ctx, id)
	must(t, err)
	ana := svc.Content.Member(member)
	oldRef := state.Positions[ana].Body
	if oldRef == "" {
		t.Fatal("the first position stored no body ref, so this test proves nothing")
	}
	must(t, svc.SavePosition(ctx, id, member, option, currentBody))

	// The erasure content_test.go documents, verbatim: the rows the current
	// state names, then the link from the name to the member id.
	state, err = svc.Get(ctx, id)
	must(t, err)
	if state.Positions[ana].Body == oldRef {
		t.Fatal("the second position did not replace the first, so this test proves nothing")
	}
	svc.Content.Delete(state.Title, state.Options[0].Title, state.Positions[ana].Body)
	svc.Content.Forget(ana)

	if got := svc.Content.Text(oldRef); got != "" {
		t.Fatalf("after erasing the member, the register's table still holds the position body they "+
			"replaced, %q, under the ref %q that the ledger's history names", got, oldRef)
	}
}
