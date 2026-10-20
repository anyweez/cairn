package ledger_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// A view rebuild lists the streams to write before it locks any of them. A
// stream first written while the rebuild is running is not in that list, so
// the rebuilt view never gets a row for it and Rebuild still reports success.
func TestQARebuildFillsAViewForAStreamWrittenDuringIt(t *testing.T) {
	agg := newCounter(t)
	view := totalsView(t, agg)
	writer := counterOn(ledger.New(agg.Store.Pool(), ledger.RequireActor(false)))

	if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}

	// The rebuild has listed the streams (just "a") and paused inside its
	// write, before it has committed.
	inWrite := make(chan struct{})
	slow := view
	slow.Write = func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
		close(inWrite)
		time.Sleep(200 * time.Millisecond)
		return view.Write(b, at, cause)
	}
	done := make(chan struct{})
	var n int
	var err error
	go func() {
		n, err = agg.Rebuild(ctx, slow)
		close(done)
	}()
	<-inWrite

	// A new stream is first written while the rebuild is running. The view is
	// not registered on the writer: this is the "view added" case Rebuild is
	// for, so only the rebuild can fill its rows.
	if _, err := writer.Handle(ctx, "b", add{2, 100}); err != nil {
		t.Fatal(err)
	}
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rebuilt %d streams, want 2 (a and b)", n)
	}

	got := fmt.Sprint(readTotals(t, agg.Store.Pool()))
	want := fmt.Sprint([]totalRow{{"a", 1, 1}, {"b", 2, 1}})
	if got != want {
		t.Fatalf("rebuilt view is %s, want %s", got, want)
	}
}

// RebuildEntries takes the same stream list before it replays any of them, so
// an entry view replay misses a stream first written while it is running.
func TestQARebuildEntriesFillsAnEntryViewForAStreamWrittenDuringIt(t *testing.T) {
	agg := newCounter(t)
	history := historyView(t, agg, "")
	writer := counterOn(ledger.New(agg.Store.Pool(), ledger.RequireActor(false)))

	if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}

	inWrite := make(chan struct{})
	slow := history
	slow.Write = func(b *ledger.Batch, before, after ledger.Loaded[counter], e ledger.Entry, events []event) error {
		close(inWrite)
		time.Sleep(200 * time.Millisecond)
		return history.Write(b, before, after, e, events)
	}
	done := make(chan struct{})
	var n int
	var err error
	go func() {
		n, err = agg.RebuildEntries(ctx, slow)
		close(done)
	}()
	<-inWrite

	if _, err := writer.Handle(ctx, "b", add{2, 100}); err != nil {
		t.Fatal(err)
	}
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rebuilt %d streams, want 2 (a and b)", n)
	}

	got := fmt.Sprint(readHistory(t, agg))
	want := fmt.Sprint([]historyRow{{"a", 1, 0, 1, 1}, {"b", 1, 0, 2, 2}})
	if got != want {
		t.Fatalf("rebuilt history is %s, want %s", got, want)
	}
}
