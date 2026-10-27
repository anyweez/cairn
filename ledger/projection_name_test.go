package ledger_test

import (
	"context"
	"strings"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

func TestAProjectionWithoutANameIsRefused(t *testing.T) {
	t.Parallel()
	p := ledger.Projection{
		Store: ledger.New(nil),
		Apply: func(context.Context, *ledger.Batch, ledger.Entry) error { return nil },
	}
	if err := p.Validate(); err == nil {
		t.Fatal("Validate accepted a projection with no Name")
	}
	if _, err := p.CatchUp(context.Background()); err == nil {
		t.Fatal("CatchUp accepted a projection with no Name")
	}
	// The same projection validates once it has a checkpoint of its own.
	p.Name = "read_models"
	if err := p.Validate(); err != nil {
		t.Fatalf("a named projection did not validate: %v", err)
	}
}

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

// A projection's Name is the primary key of cairn.checkpoints, so it is held to
// the same storability and length rules as every other text value cairn puts in
// a btree index. Otherwise every CatchUp fails on an index row instead of
// telling the caller what to change, and a name with a NUL comes back as a raw
// SQLSTATE.
func TestAProjectionNameTheLedgerCannotIndexIsRefused(t *testing.T) {
	t.Parallel()
	for name, pname := range map[string]string{
		"NUL":      "read\x00models",
		"too long": strings.Repeat("r", 1025),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := ledger.Projection{
				Store: ledger.New(nil), Name: pname,
				Apply: func(context.Context, *ledger.Batch, ledger.Entry) error { return nil },
			}
			err := p.Validate()
			if err == nil {
				t.Fatal("Validate accepted a projection name the checkpoint index cannot hold")
			}
			if !strings.Contains(err.Error(), "Name") {
				t.Fatalf("the error does not name the projection: %v", err)
			}
		})
	}
}
