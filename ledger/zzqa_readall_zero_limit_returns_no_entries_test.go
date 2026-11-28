package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// ReadAll's contract is "returns up to limit entries after the cursor, in
// transaction order". A limit of zero therefore asks for none, and a negative
// one is not a request for a default either.
//
// readAll, which Store.ReadAll shares with Projection.CatchUp, replaces any
// limit <= 0 with 100 before it builds the query. That default belongs to the
// projection — Projection.BatchSize documents it — and CatchUp computes its
// own batch, so the branch only ever changes what a public ReadAll caller
// gets. A caller paginating with a computed limit (min(pageSize, remaining))
// is handed 100 entries it did not ask for, and a cursor advanced past all of
// them, so a reader that advances by the cursor it is given silently skips
// entries it never saw.
func TestReadAllWithAZeroLimitReturnsNoEntries(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	for range 3 {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}

	start := ledger.Cursor{}
	for _, limit := range []int{0, -1} {
		entries, after, err := agg.Store.ReadAll(ctx, start, limit)
		if err != nil {
			t.Fatalf("ReadAll with limit %d: %v", limit, err)
		}
		if len(entries) != 0 {
			t.Fatalf("ReadAll with limit %d returned %d entries; up to %d means none", limit, len(entries), limit)
		}
		if after != start {
			t.Fatalf("ReadAll with limit %d moved the cursor from %+v to %+v, past entries it returned", limit, start, after)
		}
	}
}
