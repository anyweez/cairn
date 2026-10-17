package ledger_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn"
)

// A refusal is a stored entry like any other, but unlike a message, event,
// effect or result it never passes through a codec's storable-text check.
// When a decider's refusal message carries text Postgres cannot hold, the
// caller is not told the message was refused: it gets the driver's SQLSTATE.
func TestARefusalMessageTheLedgerCannotStoreIsNotARawDatabaseError(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
		// The raw NUL, not %q's escaped form: a decider formatting a value it
		// was handed is the ordinary way this text reaches a refusal.
		return cairn.Refuse[int, event, effect]("bad", "%s", "a\x00b")
	}
	_, err := agg.Handle(ctx, "a", add{1, 10})
	if !errors.Is(err, &cairn.Refusal{Code: "bad"}) {
		t.Fatalf("a refused message must be answered with its refusal, got %T: %v", err, err)
	}
}

// The other half of the same gap: text that json.Marshal rewrites. The caller
// is handed the refusal it decided, while the ledger keeps a coerced one, and
// nothing reports the difference.
func TestARefusalMessageThatIsNotUTF8ComesBackChanged(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	sent := "value " + "a\xffb"
	agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
		return cairn.Refuse[int, event, effect]("bad", "%s", sent)
	}
	h, err := agg.Handle(ctx, "a", add{1, 10})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if h.Refusal == nil {
		t.Fatalf("the refusal was not returned: %+v", h)
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Refusal == nil {
		t.Fatalf("entries: %+v", entries)
	}
	if got := entries[0].Refusal.Message; got != h.Refusal.Message {
		t.Fatalf("stored refusal %q, sent %q", got, h.Refusal.Message)
	}
}
