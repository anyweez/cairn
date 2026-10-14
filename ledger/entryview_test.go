package ledger_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// historyView keeps one row per entry: the totals before and after it, and
// what it added. references, when set, is a constraint the table adds, so a
// test can make its rows depend on a view's.
func historyView(t testing.TB, agg *ledger.Aggregate[counter, msg, int, event, effect], references string) ledger.EntryView[counter, event] {
	t.Helper()
	if _, err := agg.Store.Pool().Exec(ctx, `CREATE TABLE counter_history (
    id text NOT NULL, seq bigint NOT NULL, before int NOT NULL, after int NOT NULL, added int NOT NULL,
    PRIMARY KEY (id, seq)`+references+`)`); err != nil {
		t.Fatal(err)
	}
	return ledger.EntryView[counter, event]{
		Name: "counter_history",
		Write: func(b *ledger.Batch, before, after ledger.Loaded[counter], e ledger.Entry, events []event) error {
			for _, ev := range events {
				b.Queue(`INSERT INTO counter_history VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
					after.Stream.ID, e.Seq, before.State.Total, after.State.Total, ev.(added).N)
			}
			return nil
		},
	}
}

type historyRow struct {
	ID                   string
	Seq                  int64
	Before, After, Added int
}

func readHistory(t *testing.T, agg *ledger.Aggregate[counter, msg, int, event, effect]) []historyRow {
	t.Helper()
	rows, err := agg.Store.Pool().Query(ctx, `SELECT id, seq, before, after, added FROM counter_history ORDER BY id, seq`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[historyRow])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// checkHistory fails unless the history is one row for each of seqs 1..n on
// stream id, each picking up where the one before it left off.
func checkHistory(t *testing.T, got []historyRow, id string, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("%d history rows, want %d: %v", len(got), n, got)
	}
	total := 0
	for i, r := range got {
		if r.ID != id || r.Seq != int64(i+1) || r.Before != total || r.After != total+r.Added {
			t.Fatalf("row %d is %+v, after a total of %d", i, r, total)
		}
		total = r.After
	}
}

// A View is written once per transaction; an EntryView once per entry, even
// when one transaction records several of a stream's messages.
func TestAnEntryViewWritesARowForEveryEntryRecordedTogether(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.EntryViews = []ledger.EntryView[counter, event]{historyView(t, agg, "")}
	const n = 60
	_, errs := handleAll(agg, "a", slices.Repeat([]msg{add{1, 1000}}, n))
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	var txs int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(DISTINCT txid) FROM cairn.entries`).Scan(&txs); err != nil {
		t.Fatal(err)
	}
	if txs >= n {
		t.Fatalf("%d messages took %d transactions; none were recorded together", n, txs)
	}
	checkHistory(t, readHistory(t, agg), "a", n)
}

func TestAnEntryViewSkipsRefusals(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.EntryViews = []ledger.EntryView[counter, event]{historyView(t, agg, "")}
	for _, m := range []add{{2, 5}, {9, 5}, {3, 5}} { // the 9 is refused
		if _, err := agg.Handle(ctx, "a", m); err != nil && !errors.As(err, new(*cairn.Refusal)) {
			t.Fatal(err)
		}
	}
	got := readHistory(t, agg)
	if fmt.Sprint(got) != fmt.Sprint([]historyRow{{"a", 1, 0, 2, 2}, {"a", 3, 2, 5, 3}}) {
		t.Fatalf("history %v: a refusal has no events, so no row", got)
	}
}

func TestAnEntryViewIsWrittenThroughApply(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.EntryViews = []ledger.EntryView[counter, event]{historyView(t, agg, "")}
	apply := func(abandon bool) error {
		return agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("a")}, func(tx *ledger.Tx) error {
			at, err := agg.LoadLocked(ctx, tx, "a")
			if err != nil {
				return err
			}
			for range 2 {
				if _, err := agg.Apply(ctx, tx, &at, add{4, 100}); err != nil {
					return err
				}
			}
			if abandon {
				return errors.New("abandon")
			}
			return nil
		})
	}
	if err := apply(false); err != nil {
		t.Fatal(err)
	}
	if err := apply(true); err == nil {
		t.Fatal("the abandoned transaction should fail")
	}
	checkHistory(t, readHistory(t, agg), "a", 2)
}

// A Write that returns an error loses its own message; the messages recorded
// with it are recorded without it.
func TestAFailingEntryViewLosesOnlyItsMessage(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	history := historyView(t, agg, "")
	agg.EntryViews = []ledger.EntryView[counter, event]{{Name: history.Name,
		Write: func(b *ledger.Batch, before, after ledger.Loaded[counter], e ledger.Entry, events []event) error {
			if events[0].(added).N == 7 {
				return errors.New("no sevens")
			}
			return history.Write(b, before, after, e, events)
		}}}

	_, errs := handleAll(agg, "a", []msg{add{1, 100}, add{7, 100}, add{1, 100}, add{1, 100}})
	for i, err := range errs {
		if (i == 1) != (err != nil) {
			t.Fatalf("message %d: %v; want only the seven to fail", i, err)
		}
	}
	if !strings.Contains(errs[1].Error(), "no sevens") {
		t.Fatalf("want the entry view's error, got %v", errs[1])
	}
	checkStream(t, agg, "a", 3, 3)
	checkHistory(t, readHistory(t, agg), "a", 3)
}

// A statement an entry view queued that fails keeps its message from being
// recorded, as a view's does.
func TestAnEntryViewWhoseStatementFailsRefusesToRecordTheMessage(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	history := historyView(t, agg, "")
	agg.EntryViews = []ledger.EntryView[counter, event]{{Name: history.Name,
		Write: func(b *ledger.Batch, before, after ledger.Loaded[counter], e ledger.Entry, events []event) error {
			if events[0].(added).N == 7 {
				b.Queue(`SELECT 1/0`)
			}
			return history.Write(b, before, after, e, events)
		}}}

	_, errs := handleAll(agg, "a", []msg{add{1, 100}, add{7, 100}, add{1, 100}})
	for i, err := range errs {
		if (i == 1) != (err != nil) {
			t.Fatalf("message %d: %v; want only the seven to fail", i, err)
		}
	}
	if !strings.Contains(errs[1].Error(), "division by zero") || !strings.Contains(errs[1].Error(), "counter_history") {
		t.Fatalf("want the statement's error, naming the entry view, got %v", errs[1])
	}
	checkStream(t, agg, "a", 2, 2)
	checkHistory(t, readHistory(t, agg), "a", 2)
}

// Entry views are written after views, so their rows can reference a view's:
// the first message's history row needs the totals row it creates.
func TestAnEntryViewsRowsCanReferenceAViewsRows(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Views = []ledger.View[counter]{totalsView(t, agg)}
	agg.EntryViews = []ledger.EntryView[counter, event]{historyView(t, agg, ", FOREIGN KEY (id) REFERENCES counter_totals (id)")}
	for range 3 {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}
	checkHistory(t, readHistory(t, agg), "a", 3)
}

// A live write can't know an entry's Position or TxID, so a rebuild must not
// pass them either, or it could write rows a live write never would.
func TestAnEntryViewSeesNoPositionLiveOrRebuilding(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	v := ledger.EntryView[counter, event]{Name: "positions",
		Write: func(_ *ledger.Batch, _, _ ledger.Loaded[counter], e ledger.Entry, _ []event) error {
			if e.Position != 0 || e.TxID != 0 {
				return fmt.Errorf("entry %d has position %d, txid %d", e.Seq, e.Position, e.TxID)
			}
			return nil
		}}
	agg.EntryViews = []ledger.EntryView[counter, event]{v}
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.RebuildEntries(ctx, v); err != nil {
		t.Fatal(err)
	}
}

func TestRebuildEntriesReplaysEveryEntry(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	history := historyView(t, agg, "")
	agg.EntryViews = []ledger.EntryView[counter, event]{history}
	for _, id := range []string{"a", "b"} {
		for _, m := range []add{{2, 6}, {9, 6}, {3, 6}} { // the 9 is refused
			_, _ = agg.Handle(ctx, id, m)
		}
	}
	live := readHistory(t, agg)

	if _, err := agg.Store.Pool().Exec(ctx, `DELETE FROM counter_history`); err != nil {
		t.Fatal(err)
	}
	n, err := agg.RebuildEntries(ctx, history)
	if err != nil || n != 2 {
		t.Fatalf("rebuilt %d streams, %v; want 2", n, err)
	}
	if got := readHistory(t, agg); fmt.Sprint(got) != fmt.Sprint(live) || len(got) != 4 {
		t.Fatalf("rebuilt %v, want what was written live: %v", got, live)
	}
	// Run again, it changes nothing.
	if _, err := agg.RebuildEntries(ctx, history); err != nil {
		t.Fatal(err)
	}
	if got := readHistory(t, agg); fmt.Sprint(got) != fmt.Sprint(live) {
		t.Fatalf("a second rebuild changed the history: %v", got)
	}
}
