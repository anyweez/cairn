package ledger_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A retry of an idempotency key is answered from its first entry, and the
// exemption in answeredFromFirstEntry is written for a retry that *lost* its
// context: "Only a missing actor does". A request that carries a present actor
// which is self-consistent — the metadata and the message both name u_99 — but
// replays another actor's key is answered anyway, because the duplicate
// lookup compares the request to its own message, never to the stored entry's
// actor. So u_99 is handed u_10's stored result, with no error.
//
// The identity boundary the README documents is that an entry's result belongs
// to the actor who recorded it. A different actor that guesses or observes a
// key must not receive it.
func TestAnotherActorsIdempotencyKeyIsNotAnswered(t *testing.T) {
	t.Parallel()
	for _, cache := range []int{0, 100} {
		t.Run(fmt.Sprintf("cache=%d", cache), func(t *testing.T) {
			agg := newCounter(t)
			agg.Store = ledgertest.NewStore(t, ledger.RequireActor(true), ledger.StateCacheSize(cache))
			agg.Actor = func(m msg) string { return fmt.Sprintf("u_%d", m.(add).Cap) }

			// u_10 records add{1,10} with key k. add.Cap stands in for the
			// sender, so this entry is attributed to u_10.
			if _, err := agg.Handle(context.Background(), "a", add{1, 10}, ledger.IdempotencyKey("k")); err != nil {
				t.Fatal(err)
			}

			// u_99 sends a message that names u_99, with metadata naming u_99
			// too, and replays u_10's key.
			as := ledger.ContextWithMeta(context.Background(), ledger.Meta{Actor: "u_99"})
			h, err := agg.Handle(as, "a", add{7, 99}, ledger.IdempotencyKey("k"))
			if err == nil && h.Duplicate && h.Entry.Meta.Actor == "u_10" {
				t.Fatalf("u_99 replayed u_10's key and was handed u_10's stored result %d (entry %s#%d, actor %q)",
					h.Result, h.Entry.Stream, h.Entry.Seq, h.Entry.Meta.Actor)
			}
		})
	}
}

// Apply has the same boundary: it answers the duplicate before it can compare
// the request's actor to the stored entry's, so a composed transaction hands
// u_99 u_10's result.
func TestAnotherActorsIdempotencyKeyIsNotAnsweredByApply(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Store = ledgertest.NewStore(t, ledger.RequireActor(true))
	agg.Actor = func(m msg) string { return fmt.Sprintf("u_%d", m.(add).Cap) }

	apply := func(ctx context.Context, m msg, opts ...ledger.Option) (ledger.Handled[int], error) {
		var h ledger.Handled[int]
		err := agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("a")}, func(tx *ledger.Tx) error {
			at, err := agg.LoadLocked(ctx, tx, "a")
			if err != nil {
				return err
			}
			h, err = agg.Apply(ctx, tx, &at, m, opts...)
			return err
		})
		return h, err
	}

	if _, err := apply(context.Background(), add{1, 10}, ledger.IdempotencyKey("k")); err != nil {
		t.Fatal(err)
	}

	as := ledger.ContextWithMeta(context.Background(), ledger.Meta{Actor: "u_99"})
	h, err := apply(as, add{7, 99}, ledger.IdempotencyKey("k"))
	if err == nil && h.Duplicate && h.Entry.Meta.Actor == "u_10" {
		t.Fatalf("u_99 replayed u_10's key and Apply handed it u_10's stored result %d (actor %q)",
			h.Result, h.Entry.Meta.Actor)
	}
}
