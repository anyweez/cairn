package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// A View or EntryView is a Name and a Write function. Validate is the startup
// check that is supposed to turn a setup mistake into an error, and it checks
// the Decider's and the codecs' functions. A view with no Write passes it and
// then panics on the first message, which is the mistake Validate exists to
// catch.
func TestAViewWithNoWriteIsRefusedByValidate(t *testing.T) {
	t.Parallel()
	agg := counterOn(ledger.New(nil))
	agg.Views = []ledger.View[counter]{{Name: "totals"}}
	if err := agg.Validate(); err == nil {
		t.Fatal("a view with no Write must fail Validate")
	}

	agg = counterOn(ledger.New(nil))
	agg.EntryViews = []ledger.EntryView[counter, event]{{Name: "history"}}
	if err := agg.Validate(); err == nil {
		t.Fatal("an entry view with no Write must fail Validate")
	}
}
