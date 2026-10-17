package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The README promises that a request whose metadata names someone else is
// refused. An idempotency key that matches an earlier entry short-circuits the
// check: runGroup answers a duplicate before it looks at metaErr, so an
// impersonating request gets the first actor's result instead of
// ErrActorMismatch.
func TestAnImpersonatingRetryIsRefused(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Store = ledgertest.NewStore(t, ledger.RequireActor(true))
	// The counter's message has no "by" field, so its cap stands in for one.
	agg.Actor = func(m msg) string { return fmt.Sprintf("u_%d", m.(add).Cap) }

	// u_10 records with key k, on stream "a".
	if _, err := agg.Handle(context.Background(), "a", add{1, 10}, ledger.IdempotencyKey("k")); err != nil {
		t.Fatal(err)
	}

	// u_99 sends the same message and key while the message names u_10.
	as := ledger.ContextWithMeta(context.Background(), ledger.Meta{Actor: "u_99"})
	_, err := agg.Handle(as, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if !errors.Is(err, ledger.ErrActorMismatch) {
		t.Fatalf("an impersonating retry must be refused with ErrActorMismatch, got %v", err)
	}
}
