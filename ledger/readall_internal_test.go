package ledger

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedLog writes one entry per kind, each in its own transaction so the log
// order is the order given.
func seedLog(t *testing.T, pool *pgxpool.Pool, kinds []string) {
	t.Helper()
	ctx := context.Background()
	for i, kind := range kinds {
		stream := fmt.Sprintf("%s/s%d", kind, i)
		if _, err := pool.Exec(ctx, `INSERT INTO cairn.streams (stream, kind) VALUES ($1, $2)`, stream, kind); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO cairn.entries (stream, kind, seq, message) VALUES ($1, $2, 1, '{}')`, stream, kind); err != nil {
			t.Fatal(err)
		}
	}
}

// ReadAll on a few kinds has to look each one up in entries_kind_log instead
// of walking the global log and filtering by kind. One statement over a kind
// list can use (kind, txid, position) for one kind's range in log order but
// not for several, so the planner chooses between entries_log with a filter —
// whose cost grows with every entry of every other kind in the log — and a
// sort of the matching rows. A projection over kinds that are a small part of
// the log then scans the whole log on every poll, for a batch of one or two
// entries. A branch per kind lets each kind's own index range supply them in
// order.
//
// The plan is the behaviour, so this asks the database for it. The seeded log
// is mostly kinds the reader does not ask for, which is when the difference
// matters.
func TestReadAllOfSeveralKindsReadsTheKindIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	if err := New(pool, RequireActor(false)).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// A hundred kinds, so each is a small part of the log: the read asks for
	// three of them.
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.streams (stream, kind)
SELECT 'log/'||g, 'kind'||(g % 100) FROM generate_series(1, 20000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.entries (stream, kind, seq, message)
SELECT 'log/'||g, 'kind'||(g % 100), 1, '{}' FROM generate_series(1, 20000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE cairn.entries`); err != nil {
		t.Fatal(err)
	}

	sql, args := readAllSQL(Cursor{}, 100, []string{"kind0", "kind1", "kind2"})
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The read's fence is set in the same transaction, as readAll sends it.
	if _, err := tx.Exec(ctx, logFence); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, "EXPLAIN "+sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "entries_kind_log") {
		t.Fatalf("reading three kinds of a 20,000-entry log does not use entries_kind_log:\n%s", plan.String())
	}
}

// ReadAll with kinds is the whole log with the other kinds removed, in the
// same order and split across cursors the same way. The unfiltered read is the
// reference: it is one ordered scan, and the filtered read must agree with it
// entry for entry.
func TestReadAllWithKindsIsTheSubsequenceOfTheWholeLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	if err := New(pool, RequireActor(false)).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cycle := []string{"a", "b", "c"}
	kinds := make([]string, 0, 30)
	for i := range 30 {
		kinds = append(kinds, cycle[i%len(cycle)])
	}
	seedLog(t, pool, kinds)

	all, err := readAll(ctx, pool, Cursor{}, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	var want []Entry
	for _, e := range all {
		if e.Stream.Kind == "a" || e.Stream.Kind == "c" {
			want = append(want, e)
		}
	}

	// Paged, with a repeated kind, as a caller passing its own list would.
	var got []Entry
	cursor := Cursor{}
	for {
		page, err := readAll(ctx, pool, cursor, 4, []string{"a", "c", "a"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
		cursor = Cursor{TxID: page[len(page)-1].TxID, Position: page[len(page)-1].Position}
	}
	if key := func(entries []Entry) string {
		var b strings.Builder
		for _, e := range entries {
			fmt.Fprintf(&b, "%s#%d ", e.Stream, e.Seq)
		}
		return b.String()
	}; key(got) != key(want) {
		t.Fatalf("kinds [a c] read %s; the whole log's subsequence is %s", key(got), key(want))
	}
}
