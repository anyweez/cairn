package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// Two views that share a helper queue the same SQL. When the later one's
// argument cannot be encoded, the error names the wrong view: Batch.failed
// matches a preprocessing error's SQL against the first queued statement with
// that SQL, not the statement that failed.
func TestABatchNamesTheViewWhoseStatementFailed(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{
		{Name: "first", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`SELECT $1::int`, 1)
			return nil
		}},
		{Name: "second", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`SELECT $1::int`, make(chan int))
			return nil
		}},
	}
	_, err := agg.Handle(ctx, "a", add{1, 10})
	if err == nil {
		t.Fatal("want an encoding error")
	}
	if !strings.Contains(err.Error(), "view second at counter/a#1") {
		t.Fatalf("the error must name the view whose statement failed, got %v", err)
	}
}

// The same defect the other way round: the first of two views with the same
// SQL has the argument that cannot be encoded. Naming the last statement with
// that SQL would pass the test above and be no better.
func TestABatchNamesTheFirstViewWhenItsStatementFailed(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{
		{Name: "first", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`SELECT $1::int`, make(chan int))
			return nil
		}},
		{Name: "second", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`SELECT $1::int`, 1)
			return nil
		}},
	}
	_, err := agg.Handle(ctx, "a", add{1, 10})
	if err == nil {
		t.Fatal("want an encoding error")
	}
	if !strings.Contains(err.Error(), "view first at counter/a#1") {
		t.Fatalf("the error must name the view whose statement failed, got %v", err)
	}
}

// The cached path, which is the default one once a stream has been read,
// sends BEGIN, the message and the views in one batch. With the simple
// protocol pgx fails to build that batch before sending any of it, so nothing
// ran and nothing was recorded: the error must name the view and must not
// claim the message was recorded.
func TestABatchNamesTheViewWhenTheStreamIsCached(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	agg.Views = []ledger.View[counter]{
		{Name: "first", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`SELECT $1::int`, 1)
			return nil
		}},
		{Name: "second", Write: func(b *ledger.Batch, _ ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`SELECT $1::int`, make(chan int))
			return nil
		}},
	}
	_, err := agg.Handle(ctx, "a", add{1, 10})
	if err == nil {
		t.Fatal("want an encoding error")
	}
	if errors.Is(err, ledger.ErrRecorded) {
		t.Fatalf("the batch was never sent, so the message was not recorded, got %v", err)
	}
	if !strings.Contains(err.Error(), "view second at counter/a#2") {
		t.Fatalf("the error must name the view whose statement failed, got %v", err)
	}
}
