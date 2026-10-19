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
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn/ledger"
)

// NewStore creates a fresh database, migrates it, and drops it when the test
// ends. Messages need no actor, unless opts include ledger.RequireActor(true).
func NewStore(t testing.TB, opts ...ledger.StoreOption) *ledger.Store {
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
		rollbackPrepared(context.Background(), url, name)
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})

	store := ledger.New(pool, append([]ledger.StoreOption{ledger.RequireActor(false)}, opts...)...)
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return store
}

// rollbackPrepared rolls back any prepared transaction left in database name,
// so the subsequent DROP cannot fail with "being used by prepared
// transactions". A prepared transaction must be finished from the database
// that prepared it, so this opens a connection to that database; a test
// process killed between PREPARE and COMMIT leaves one behind even though
// t.Cleanup never runs.
func rollbackPrepared(ctx context.Context, url, name string) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return
	}
	cfg.Database = name
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return
	}
	defer conn.Close(ctx)

	rows, err := conn.Query(ctx, `SELECT gid FROM pg_prepared_xacts WHERE database = current_database()`)
	if err != nil {
		return
	}
	var gids []string
	for rows.Next() {
		var gid string
		if rows.Scan(&gid) == nil {
			gids = append(gids, gid)
		}
	}
	rows.Close()
	for _, gid := range gids {
		_, _ = conn.Exec(ctx, `ROLLBACK PREPARED '`+strings.ReplaceAll(gid, "'", "''")+`'`)
	}
}
