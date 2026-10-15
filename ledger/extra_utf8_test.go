package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// A caller may put text in Meta.Extra. If that text is not valid UTF-8, the
// ledger must either refuse it or store it unchanged; it must not report
// success and hand back different text.
func TestExtraTextThatIsNotUTF8ComesBackChanged(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	const sent = "v\xff"
	_, err := agg.Handle(ctx, "a", add{1, 100}, ledger.WithMeta(ledger.Meta{
		Actor: "u_ana",
		Extra: map[string]any{"note": sent},
	}))
	if err != nil {
		return // refusing it is fine; silently altering it is not
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := entries[0].Meta.Extra["note"].(string)
	if got != sent {
		t.Fatalf("stored %q, sent %q", got, sent)
	}
}
