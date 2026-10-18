package decisions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The reference index holds absolute text for a decision and the outbox
// delivers effects at least once but not in order. When an earlier
// IndexReferences effect is retried after a later one has been delivered, a
// consumer that just stores the text it is handed goes back to text the
// decision no longer has. The index must drop the older delivery.
func TestIndexTextDoesNotGoBackwardsOnARetriedEffect(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	id, err := svc.Create(ctx, "ana", "first", "yes")
	if err != nil {
		t.Fatal(err)
	}
	// A second message changes the indexed text; this queues a second
	// IndexReferences effect for the same decision.
	if _, err := svc.Register.Handle(ctx, id, Edit{By: "ana", Title: "second"}); err != nil {
		t.Fatal(err)
	}

	index := NewReferenceIndex()
	deliveries := 0
	d := &ledger.Dispatcher[Effect]{
		Store:   svc.Register.Store,
		Kind:    "decision",
		Effects: svc.Register.Codecs.Effects,
		Backoff: func(int) time.Duration { return time.Millisecond },
		Handle: func(_ context.Context, dl ledger.Delivery[Effect]) error {
			deliveries++
			if deliveries == 1 {
				return errors.New("index unavailable")
			}
			// The retried create effect arrives after the edit's. The index
			// drops it because its entry is older, not because of the order it
			// is handed.
			index.Apply(dl.Seq, dl.Effect.(IndexReferences))
			return nil
		},
	}

	// First drain: the create's effect fails, the edit's effect succeeds.
	if _, err := d.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	// Second drain: the failed effect is retried and overwrites the newer text.
	if _, err := d.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}

	state, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := indexText(state)
	if got := index.Text(id); got != want {
		t.Fatalf("the index holds %q, but the decision's text is %q", got, want)
	}
}
