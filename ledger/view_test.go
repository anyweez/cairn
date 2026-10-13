package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// totalsView keeps one row per counter: its total, and the seq it was written
// at, so a test can tell whether a write happened.
func totalsView(t *testing.T, agg *ledger.Aggregate[counter, msg, int, event, effect]) ledger.View[counter] {
	t.Helper()
	if _, err := agg.Store.Pool().Exec(ctx,
		`CREATE TABLE counter_totals (id text PRIMARY KEY, total int NOT NULL, seq bigint NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	return ledger.View[counter]{
		Name: "counter_totals",
		Write: func(b *ledger.Batch, at ledger.Loaded[counter], _ ledger.Entry) error {
			b.Queue(`
INSERT INTO counter_totals VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET total = excluded.total, seq = excluded.seq`, at.Stream.ID, at.State.Total, at.Seq)
			return nil
		},
	}
}

type totalRow struct {
	ID    string
	Total int
	Seq   int64
}

func readTotals(t *testing.T, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}) []totalRow {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT id, total, seq FROM counter_totals ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[totalRow])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAViewShowsAWriteAsSoonAsHandleReturns(t *testing.T) {
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}

	h, err := agg.Handle(ctx, "a", add{3, 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(readTotals(t, agg.Store.Pool())); got != fmt.Sprint([]totalRow{{"a", 3, h.Entry.Seq}}) {
		t.Fatalf("the view should hold the write Handle just returned, got %s", got)
	}
}

func TestAViewCommitsAndRollsBackWithItsMessage(t *testing.T) {
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}
	errAbandon := errors.New("abandon")

	err := agg.Store.Transact(ctx, func(tx *ledger.Tx) error {
		if err := tx.Lock(ctx, agg.Stream("a")); err != nil {
			return err
		}
		at, err := agg.LoadLocked(ctx, tx, "a")
		if err != nil {
			return err
		}
		if _, err := agg.Apply(ctx, tx, &at, add{4, 10}); err != nil {
			return err
		}
		// Visible to the rest of the transaction that wrote it.
		if got := readTotals(t, tx); len(got) != 1 || got[0].Total != 4 {
			t.Errorf("inside the transaction the view holds %v", got)
		}
		return errAbandon
	})
	if !errors.Is(err, errAbandon) {
		t.Fatal(err)
	}
	if got := readTotals(t, agg.Store.Pool()); len(got) != 0 {
		t.Fatalf("a rolled-back message left %v in the view", got)
	}
}

func TestARefusalLeavesTheViewAlone(t *testing.T) {
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}

	if _, err := agg.Handle(ctx, "new", add{50, 10}); !errors.Is(err, &cairn.Refusal{Code: "over_cap"}) {
		t.Fatalf("want a refusal, got %v", err)
	}
	if _, err := agg.Handle(ctx, "a", add{3, 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Handle(ctx, "a", add{50, 10}); err == nil {
		t.Fatal("want a refusal")
	}
	// No row for the stream that only refused; a's row is from its first entry.
	if got := fmt.Sprint(readTotals(t, agg.Store.Pool())); got != fmt.Sprint([]totalRow{{"a", 3, 1}}) {
		t.Fatalf("refusals wrote to the view: %s", got)
	}
}

func TestAFailingViewRefusesToRecordTheMessage(t *testing.T) {
	agg := newCounter(t)
	broken := errors.New("view is broken")
	agg.Views = []ledger.View[counter]{{Name: "broken", Write: func(*ledger.Batch, ledger.Loaded[counter], ledger.Entry) error {
		return broken
	}}}

	if _, err := agg.Handle(ctx, "a", add{3, 10}); !errors.Is(err, broken) {
		t.Fatalf("want the view's error, got %v", err)
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the message was recorded without its view: %v %v", entries, err)
	}
}

// A statement that fails in the database aborts the message too, and the
// error names the view that queued it, though every view's statements were
// sent together. A statement can fail as the batch is prepared, before any
// runs, or as it runs.
func TestAViewWhoseStatementFailsRefusesToRecordTheMessage(t *testing.T) {
	for _, failing := range []string{
		`INSERT INTO no_such_table VALUES ($1)`,
		`INSERT INTO counter_totals VALUES ($1 || '-copy', NULL, 0)`,
	} {
		agg := newCounter(t)
		agg.Views = []ledger.View[counter]{
			totalsView(t, agg),
			{Name: "bad", Write: func(b *ledger.Batch, at ledger.Loaded[counter], _ ledger.Entry) error {
				b.Queue(`SELECT 1`)
				b.Queue(failing, at.Stream.ID)
				return nil
			}},
		}

		_, err := agg.Handle(ctx, "a", add{3, 10})
		if err == nil || !strings.Contains(err.Error(), "view bad at counter/a#1: statement 1:") {
			t.Fatalf("%s: want an error naming the view and its statement, got %v", failing, err)
		}
		entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s: the message was recorded without its view: %v %v", failing, entries, err)
		}
		if got := readTotals(t, agg.Store.Pool()); len(got) != 0 {
			t.Fatalf("%s: the other view's write survived: %v", failing, got)
		}
	}
}

func TestRebuildFillsAViewFromCurrentState(t *testing.T) {
	agg := newCounter(t)
	view := totalsView(t, agg)

	// Written before the view existed, including a stream that only refused
	// and a refusal after a's last change, which a live view never sees.
	for _, m := range []struct {
		id string
		n  int
	}{{"a", 2}, {"b", 5}, {"a", 3}, {"refused", 50}, {"a", 50}} {
		_, _ = agg.Handle(ctx, m.id, add{m.n, 10})
	}

	want := fmt.Sprint([]totalRow{{"a", 5, 2}, {"b", 5, 1}})
	for range 2 {
		n, err := agg.Rebuild(ctx, view)
		if err != nil || n != 2 {
			t.Fatalf("rebuilt %d streams, %v", n, err)
		}
		if got := fmt.Sprint(readTotals(t, agg.Store.Pool())); got != want {
			t.Fatalf("rebuilt view %s, want %s", got, want)
		}
	}
}

// Rebuild locks each stream while it reads and writes, so a message that
// arrives mid-rebuild waits, and its own write lands last.
func TestAWriteDuringRebuildIsNotOverwritten(t *testing.T) {
	agg := newCounter(t)
	view := totalsView(t, agg)
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	agg.Views = []ledger.View[counter]{view}

	// The rebuild has read the stream, and pauses before writing its row.
	read := make(chan struct{})
	slow := view
	slow.Write = func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
		close(read)
		time.Sleep(200 * time.Millisecond)
		return view.Write(b, at, cause)
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := agg.Rebuild(ctx, slow); err != nil {
			t.Error(err)
		}
	})
	<-read
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	if got := fmt.Sprint(readTotals(t, agg.Store.Pool())); got != fmt.Sprint([]totalRow{{"a", 2, 2}}) {
		t.Fatalf("the rebuild overwrote the newer write: %s", got)
	}
}
