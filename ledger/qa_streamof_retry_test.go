package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// Aggregate.StreamOf promises that a message naming a stream is refused with
// ErrStreamMismatch when it is recorded anywhere else, and that nothing is
// recorded. The idempotency lookup short-circuits that check: a message whose
// key matches an entry on the stream it is being applied to is answered from
// that entry, even though it names a different stream. Nothing new is written,
// but the caller is told a message naming stream "b" succeeded on stream "a",
// instead of being refused.
//
// This is the same class as t2644 (the duplicate lookup skipped the actor
// check); StreamOf was added after that fix, so it has the same omission.
func TestAMessageNamingAnotherStreamIsNotAnsweredAsADuplicate(t *testing.T) {
	for _, cache := range []int{0, 100} {
		t.Run(fmt.Sprintf("cache=%d", cache), func(t *testing.T) {
			agg := newCounter(t)
			agg.Store = ledgertest.NewStore(t, ledger.RequireActor(false), ledger.StateCacheSize(cache))
			// add.Cap names the stream: Cap 1 names "a", Cap 2 names "b".
			agg.StreamOf = func(m msg) string {
				if m.(add).Cap == 1 {
					return "a"
				}
				return "b"
			}

			// The first message names the stream it is recorded on, and holds
			// key "k" there.
			if _, err := agg.Handle(context.Background(), "a", add{1, 1}, ledger.IdempotencyKey("k")); err != nil {
				t.Fatal(err)
			}

			// A second message names "b" but is applied to "a" with the same
			// key. Its identity is wrong, so it must be refused; answering it
			// from a's entry tells the caller it was recorded on "a" when it
			// was not.
			h, err := agg.Handle(context.Background(), "a", add{2, 2}, ledger.IdempotencyKey("k"))
			if !errors.Is(err, ledger.ErrStreamMismatch) {
				t.Fatalf("a message naming stream %q was answered as %+v with err %v; want ErrStreamMismatch",
					"b", h, err)
			}
			entries, err := agg.Store.ReadStream(context.Background(), agg.Stream("a"), 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("the mismatched message was recorded: %d entries", len(entries))
			}
		})
	}
}
