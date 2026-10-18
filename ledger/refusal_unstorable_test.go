package ledger_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn"
)

// A refusal is not carried by a codec, so its own text is checked in the
// ledger. When it cannot be stored at all — a NUL, which jsonb cannot
// represent, or a code that is not valid UTF-8, which Verify compares and
// errors.Is matches — the decider's refusal is returned without an entry,
// rather than the driver's error or an entry holding rewritten text.
func TestARefusalTheLedgerCannotStoreIsAnsweredWithoutAnEntry(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		code string
		text string
	}{
		{"a NUL in the message", "bad", "a\x00b"},
		{"a code that is not UTF-8", "bad\xff", "no"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			agg := newCounter(t)
			agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
				return cairn.Refuse[int, event, effect](tc.code, "%s", tc.text)
			}
			_, err := agg.Handle(ctx, "a", add{1, 10})
			if !errors.Is(err, &cairn.Refusal{Code: tc.code}) {
				t.Fatalf("want the decider's refusal, got %T: %v", err, err)
			}
			entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("a refusal the ledger cannot store was recorded: %+v", entries)
			}
		})
	}
}
