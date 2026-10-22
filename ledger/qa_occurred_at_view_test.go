package ledger_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// An entry view is handed the entry a live write made, and RebuildEntries hands
// it the same entry again (ledger/entryview.go: "a rebuilt row is the row a live
// write made"). A timestamp the view is handed live must therefore be the one
// the ledger stores, or an audit table changes when it is rebuilt.
//
// Meta.OccurredAt is the producer's time, passed to the occurred_at timestamptz
// column as it arrived. timestamptz keeps microseconds, so the stored entry's
// value is cut to microseconds while the live write is handed the caller's
// nanoseconds; RecordedAt is cut by recordingTime for exactly this reason.
func TestAnEntryViewsRowSurvivesARebuildUnchanged(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Store.Pool().Exec(ctx,
		`CREATE TABLE counter_meta (id text NOT NULL, seq bigint NOT NULL, meta text NOT NULL, PRIMARY KEY (id, seq))`); err != nil {
		t.Fatal(err)
	}

	// An audit trail that keeps the whole entry's metadata, as an audit trail does.
	audit := ledger.EntryView[counter, event]{Name: "counter_meta",
		Write: func(b *ledger.Batch, _, after ledger.Loaded[counter], e ledger.Entry, _ []event) error {
			data, err := json.Marshal(e.Meta)
			if err != nil {
				return err
			}
			b.Queue(`INSERT INTO counter_meta VALUES ($1, $2, $3) ON CONFLICT (id, seq) DO NOTHING`,
				after.Stream.ID, e.Seq, string(data))
			return nil
		}}
	agg.EntryViews = []ledger.EntryView[counter, event]{audit}

	occurred := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	mctx := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_alice", OccurredAt: occurred})
	if _, err := agg.Handle(mctx, "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}
	live := readMetaRows(t, agg)

	if _, err := agg.Store.Pool().Exec(ctx, `DELETE FROM counter_meta`); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.RebuildEntries(ctx, audit); err != nil {
		t.Fatal(err)
	}
	rebuilt := readMetaRows(t, agg)

	if fmt.Sprint(rebuilt) != fmt.Sprint(live) {
		t.Fatalf("rebuilding the entry view changed its rows:\n live:    %v\n rebuilt: %v", live, rebuilt)
	}
}

func readMetaRows(t *testing.T, agg *ledger.Aggregate[counter, msg, int, event, effect]) []string {
	t.Helper()
	rows, err := agg.Store.Pool().Query(ctx, `SELECT meta FROM counter_meta ORDER BY id, seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
