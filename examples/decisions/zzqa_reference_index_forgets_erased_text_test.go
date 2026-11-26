package decisions

import (
	"context"
	"strings"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// content_test.go documents the register's erasure procedure: delete the rows
// the decision's state names, then forget the name behind its member id. The
// reference index is the copy of those words a reader actually searches, so
// the same deletion has to clear it. It keeps the refs IndexReferences named
// rather than the words, and reads them from Content when asked, so deleting a
// row leaves the index with no text for it. An index that stored the words it
// was handed would go on answering with the erased question and rationale
// forever: no later effect names the decision.
//
// This is what the example's own erasure test leaves out: it holds the ledger
// to the promise, not the read model the example maintains from it.
func TestReferenceIndexForgetsErasedText(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))

	const (
		member    = "ana.sorensen@example.com"
		question  = "Should we ship the beta to Ana's team?"
		rationale = "Ana's team has the capacity, so we ship on Friday"
	)

	id, err := svc.Create(ctx, member, question, "ship it")
	must(t, err)
	state, err := svc.Get(ctx, id)
	must(t, err)
	must(t, svc.SavePosition(ctx, id, member, state.Options[0].ID, "we can support it"))
	must(t, svc.RecordOutcome(ctx, id, member, state.Options[0].ID, rationale))

	// Deliver the register's own effects the way an application does; the
	// index resolves the refs through Content exactly as ReferenceIndex
	// documents.
	index := NewReferenceIndex(svc.Content)
	dispatcher := &ledger.Dispatcher[Effect]{
		Store: svc.Register.Store, Kind: "decision", Effects: svc.Register.Codecs.Effects,
		Handle: func(_ context.Context, d ledger.Delivery[Effect]) error {
			index.Apply(d.Seq, d.Effect.(IndexReferences))
			return nil
		},
	}
	for {
		n, err := dispatcher.DrainOnce(ctx)
		must(t, err)
		if n == 0 {
			break
		}
	}

	// The premise: the index held the words before the deletion request, so a
	// failure below is the deletion not reaching it rather than it never
	// having held them.
	before := index.Text(id)
	if !strings.Contains(before, question) || !strings.Contains(before, rationale) {
		t.Fatalf("the index held %q before the deletion request, so this test proves nothing", before)
	}

	// The erasure content_test.go documents: delete the text rows the state
	// names, then destroy the link from the name to the member id.
	state, err = svc.Get(ctx, id)
	must(t, err)
	ana := svc.Content.Member(member)
	svc.Content.Delete(state.Title, state.Options[0].Title, state.Positions[ana].Body, state.Outcome.Rationale)
	svc.Content.Forget(ana)

	if got := index.Text(id); strings.Contains(got, question) || strings.Contains(got, rationale) {
		t.Fatalf("after erasing the decision's rows and member, the reference index still holds %q; "+
			"a deletion request is not honoured while the read model keeps the words", got)
	}
}
