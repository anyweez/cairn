package ledger_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
)

// Validate needs no database: it reads the aggregate alone.
func TestValidateNamesTheFirstMistake(t *testing.T) {
	t.Parallel()
	complete := func() *ledger.Aggregate[counter, msg, int, event, effect] {
		agg := counterOn(ledger.New(nil))
		agg.Views = []ledger.View[counter]{{Name: "totals", Write: func(*ledger.Batch, ledger.Loaded[counter], ledger.Entry) error { return nil }}}
		return agg
	}
	if err := complete().Validate(); err != nil {
		t.Fatalf("a complete aggregate: %v", err)
	}
	cases := map[string]func(*ledger.Aggregate[counter, msg, int, event, effect]){
		"Store is nil":           func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Store = nil },
		"Kind must be non-empty": func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Kind = "" },
		"contain no slash":       func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Kind = "a/b" },
		"Decider needs":          func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Decider.Evolve = nil },
		"Codecs is nil":          func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Codecs = nil },
		"Codecs.Results is nil": func(a *ledger.Aggregate[counter, msg, int, event, effect]) {
			a.Codecs = &codec.Set[msg, int, event, effect]{Messages: a.Codecs.Messages, Events: a.Codecs.Events, Effects: a.Codecs.Effects}
		},
		"a view has no Name":         func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Views[0].Name = "" },
		"two views are named totals": func(a *ledger.Aggregate[counter, msg, int, event, effect]) { a.Views = append(a.Views, a.Views[0]) },
		"an entry view has no Name": func(a *ledger.Aggregate[counter, msg, int, event, effect]) {
			a.EntryViews = []ledger.EntryView[counter, event]{{}}
		},
		"two views are named totals (entry)": func(a *ledger.Aggregate[counter, msg, int, event, effect]) {
			a.EntryViews = []ledger.EntryView[counter, event]{{Name: "totals"}}
		},
	}
	for want, breakIt := range cases {
		agg := complete()
		breakIt(agg)
		err := agg.Validate()
		if err == nil || !strings.Contains(err.Error(), strings.TrimSuffix(want, " (entry)")) {
			t.Errorf("%s: got %v", want, err)
		}
	}
}

// A Kind reaches a text parameter on the paths that filter by Kind alone
// (Rebuild, RebuildEntries, Streams, ReadAll), so one Postgres cannot store is
// a setup mistake Validate reports, not a raw SQLSTATE from the driver.
func TestValidateRefusesAnUnstorableKind(t *testing.T) {
	t.Parallel()
	for name, kind := range map[string]string{
		"NUL":          "counter\x00x",
		"invalid utf8": "counter\xffx",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			agg := counterOn(ledger.New(nil))
			agg.Kind = kind
			if err := agg.Validate(); err == nil || !strings.Contains(err.Error(), "Kind is not valid UTF-8") {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

// Every method validates first, so a mistake is an error, not a panic.
func TestMethodsValidateBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	agg := counterOn(ledger.New(nil))
	agg.Codecs.Messages = nil
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err == nil || !strings.Contains(err.Error(), "Codecs.Messages is nil") {
		t.Fatalf("Handle: %v", err)
	}
	if _, err := agg.Load(ctx, "a"); err == nil || !strings.Contains(err.Error(), "Codecs.Messages is nil") {
		t.Fatalf("Load: %v", err)
	}
	if _, err := agg.VerifyAll(ctx); err == nil || !strings.Contains(err.Error(), "Codecs.Messages is nil") {
		t.Fatalf("VerifyAll: %v", err)
	}
}

// A StateVersion is half the primary key of cairn.state_snapshots. An
// unstorable or over-long one fails the snapshot write with a raw SQLSTATE, and
// since a snapshot comes due again on every later message the stream then
// records nothing more. Validate refuses it up front, naming the value.
func TestValidateRefusesAStateVersionTheLedgerCannotIndex(t *testing.T) {
	t.Parallel()
	for name, version := range map[string]string{
		"NUL":      "v\x00",
		"too long": strings.Repeat("v", 1025),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			agg := counterOn(ledger.New(nil))
			agg.StateVersion = version
			err := agg.Validate()
			if err == nil {
				t.Fatal("Validate accepted a state version the snapshot index cannot hold")
			}
			if msg := err.Error(); !strings.Contains(msg, "version") || !strings.Contains(msg, "snapshot") {
				t.Fatalf("the error does not name the state version: %v", err)
			}
		})
	}
}
