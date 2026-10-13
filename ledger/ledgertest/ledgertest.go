// Package ledgertest gives each test its own migrated ledger database.
//
// It needs a Postgres server to create databases in, named by
// CAIRN_TEST_DATABASE_URL (scripts/test-postgres.sh starts one). Without it the
// test is skipped, unless CAIRN_REQUIRE_DATABASE is set, as it should be in CI.
package ledgertest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn/ledger"
)

// NewStore creates a fresh database, migrates it, and drops it when the test ends.
func NewStore(t testing.TB) *ledger.Store {
	t.Helper()
	url := os.Getenv("CAIRN_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CAIRN_REQUIRE_DATABASE") != "" {
			t.Fatal("CAIRN_TEST_DATABASE_URL is not set")
		}
		t.Skip("CAIRN_TEST_DATABASE_URL is not set; run scripts/test-postgres.sh")
	}
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting to %s: %v", url, err)
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "cairn_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("creating database: %v", err)
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("opening %s: %v", name, err)
	}

	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})

	store := ledger.New(pool)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return store
}
