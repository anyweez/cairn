package ledger_test

import (
	"context"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// A Projection's Name keys its checkpoint in cairn.checkpoints. Two
// projections built without a Name (the zero value) share the empty-string
// checkpoint: the first applies the log and advances it, and the second then
// applies nothing at all, leaving its read model silently empty.
//
// An unnamed projection is a setup mistake, and the library must not let one
// silently consume another's place in the log. Either CatchUp refuses a
// projection with no Name, or the entries reach its Apply; what it must not
// do is report success having applied nothing.
func TestAnUnnamedProjectionDoesNotSilentlyShareACheckpoint(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	for _, id := range []string{"a", "b"} {
		if _, err := agg.Handle(ctx, id, add{1, 10}); err != nil {
			t.Fatal(err)
		}
	}
	for _, ddl := range []string{
		`CREATE TABLE proj_one (stream text PRIMARY KEY)`,
		`CREATE TABLE proj_two (stream text PRIMARY KEY)`,
	} {
		if _, err := agg.Store.Pool().Exec(ctx, ddl); err != nil {
			t.Fatal(err)
		}
	}

	one := ledger.Projection{Store: agg.Store, Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
		b.Queue(`INSERT INTO proj_one VALUES ($1) ON CONFLICT DO NOTHING`, e.Stream.String())
		return nil
	}}
	two := ledger.Projection{Store: agg.Store, Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
		b.Queue(`INSERT INTO proj_two VALUES ($1) ON CONFLICT DO NOTHING`, e.Stream.String())
		return nil
	}}

	if _, err := one.CatchUp(ctx); err != nil {
		// A projection with no Name may be refused outright; that is a
		// contract the test accepts too.
		return
	}
	if _, err := two.CatchUp(ctx); err != nil {
		t.Fatalf("second unnamed projection: %v", err)
	}

	var applied int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(*) FROM proj_two`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 2 {
		t.Fatalf("the second unnamed projection applied %d of 2 entries: it shared the first's checkpoint", applied)
	}
}
