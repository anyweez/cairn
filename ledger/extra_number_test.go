package ledger_test

import (
	"encoding/json"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// Meta.Extra is stored as JSON, so a caller's number survives the write
// exactly. But the entry is read back by decoding every JSON number into a Go
// `any`, which json.Unmarshal gives as a float64. A number past 2^53 therefore
// comes back as a different value than the caller put in: the write is not
// refused and reports no error, and the caller is handed a number it never
// sent. An identifier such as a snowflake is well past 2^53.
func TestExtraLargeIntegerComesBackChanged(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	const sent = int64(9007199254740993) // 2^53 + 1, not exactly a float64
	_, err := agg.Handle(ctx, "a", add{1, 100}, ledger.WithMeta(ledger.Meta{
		Actor: "u_ana",
		Extra: map[string]any{"n": sent},
	}))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var got int64
	switch v := entries[0].Meta.Extra["n"].(type) {
	case int64:
		got = v
	case float64:
		got = int64(v)
	case json.Number:
		if got, err = v.Int64(); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("stored %v of type %T, sent %d", v, v, sent)
	}
	if got != sent {
		t.Fatalf("stored %d, sent %d", got, sent)
	}
}
