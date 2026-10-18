package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// An idempotency key guards one stream's retries, not a request ID globally:
// the same key on another stream is a new message, while on its own stream it
// keeps answering with the first message's answer even when the retry carries
// a different one.
func TestAnIdempotencyKeyIsScopedToItsStream(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	const key = "req-1"

	first, err := agg.Handle(ctx, "a", add{3, 10}, ledger.IdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}

	other, err := agg.Handle(ctx, "b", add{3, 10}, ledger.IdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if other.Duplicate {
		t.Fatalf("key %q on another stream was answered as a duplicate: %+v", key, other)
	}
	if other.Entry.Seq != 1 || other.Result != 3 {
		t.Fatalf("the second stream recorded %+v", other)
	}

	// The retry carries a different, acceptable message; the key still wins.
	again, err := agg.Handle(ctx, "a", add{5, 1000}, ledger.IdempotencyKey(key))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.Result != first.Result || again.Entry.Seq != first.Entry.Seq {
		t.Fatalf("reusing the key on its stream: %+v, want the first answer %+v", again, first)
	}

	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("stream a has %d entries, want 1: the retry recorded one", len(entries))
	}
}
