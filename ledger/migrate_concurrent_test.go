package ledger

import (
	"context"
	"sync"
	"testing"
)

// Every process calls Migrate at startup, so concurrent callers on one fresh
// database must serialize on the advisory lock and converge on one schema:
// each migration recorded exactly once and no caller failing on the one that
// beat it.
func TestConcurrentMigratesConvergeOnOneSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)

	const callers = 5
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() {
			errs[i] = New(pool, RequireActor(false)).Migrate(ctx)
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
	}

	var versions, max int
	if err := pool.QueryRow(ctx,
		`SELECT count(*), max(version) FROM cairn.schema_version`).Scan(&versions, &max); err != nil {
		t.Fatal(err)
	}
	if versions != len(migrations) || max != len(migrations) {
		t.Fatalf("schema_version has %d rows up to %d; want %d rows up to %d",
			versions, max, len(migrations), len(migrations))
	}
	// The schema the callers converged on is usable.
	if _, err := pool.Exec(ctx,
		`INSERT INTO cairn.streams (stream, kind, seq) VALUES ('counter/a', 'counter', 1)`); err != nil {
		t.Fatal(err)
	}
}

// Migrate runs on every startup, so a second call must change nothing. The
// rows a running system wrote before it are left alone.
func TestMigratingAgainChangesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	var before string
	if err := pool.QueryRow(ctx, `SELECT max(applied_at)::text FROM cairn.schema_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO cairn.streams (stream, kind, seq) VALUES ('counter/a', 'counter', 1)`); err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("the second migration: %v", err)
	}

	var versions int
	var after string
	if err := pool.QueryRow(ctx,
		`SELECT count(*), max(applied_at)::text FROM cairn.schema_version`).Scan(&versions, &after); err != nil {
		t.Fatal(err)
	}
	if versions != len(migrations) || after != before {
		t.Fatalf("the second migration ran: %d versions, applied_at %s → %s", versions, before, after)
	}
	var streams int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM cairn.streams WHERE stream = 'counter/a'`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if streams != 1 {
		t.Fatalf("a row written before the second migration is gone: %d rows", streams)
	}
}
