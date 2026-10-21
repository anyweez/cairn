package ledger_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// A metadata Extra value can hold a JSON number that Postgres stores happily
// and no float64 can represent. Writing it succeeds, and every later read of
// the stream — and of the global log the projections consume — fails at that
// entry, forever: entries are append-only, so nothing can be repaired.
func TestAnExtraNumberTooLargeForFloat64LeavesTheStreamUnreadable(t *testing.T) {
	agg := newCounter(t)

	const id = "payload"
	// A caller that decodes a request with json.Decoder.UseNumber, or any value
	// whose MarshalJSON writes a large number, puts a json.Number in Extra.
	// 1e400 is a valid JSON number and a valid jsonb numeric; it is not a
	// float64.
	if _, err := agg.Handle(ctx, id, add{1, 100}, ledger.WithMeta(ledger.Meta{
		Actor: "u_ana",
		Extra: map[string]any{"amount": json.Number("1e400")},
	})); err != nil {
		t.Fatalf("the write was refused: %v", err)
	}
	// A later message on the same stream is recorded too.
	if _, err := agg.Handle(ctx, id, add{1, 100}, ledger.WithMeta(ledger.Meta{Actor: "u_ana"})); err != nil {
		t.Fatalf("a later write was refused: %v", err)
	}

	if _, err := agg.Store.ReadStream(ctx, agg.Stream(id), 0); err != nil {
		t.Fatalf("the stream cannot be read after a write that reported success, and its later entries are unreadable with it: %v", err)
	}

	// The global log is what every projection consumes; one such entry stops
	// the reader for every stream, not just this one. Its visibility waits on
	// transactions open elsewhere on the server, so only an error is asserted.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, cursor, err := agg.Store.ReadAll(ctx, ledger.Cursor{}, 10)
		if err != nil {
			t.Fatalf("reading the global log after the entry was recorded: %v", err)
		}
		for _, e := range entries {
			if e.Stream == agg.Stream(id) && e.Seq == 1 {
				return // the entry is visible in the log, and the log read it
			}
		}
		_ = cursor
		time.Sleep(50 * time.Millisecond)
	}
}
