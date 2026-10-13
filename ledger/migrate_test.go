package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entries written before a migration must read back after it. This builds a
// database at version 1 by hand, with an entry in it, then migrates.
func TestMigratingAPopulatedLedgerKeepsItsEntriesReadable(t *testing.T) {
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
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "cairn_upgrade_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(context.Background())
	})

	if _, err := pool.Exec(ctx, `
CREATE SCHEMA cairn;
CREATE TABLE cairn.schema_version (version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, migrations[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.schema_version (version) VALUES (1);
INSERT INTO cairn.streams (stream, kind, seq) VALUES ('counter/a', 'counter', 1);
INSERT INTO cairn.entries (stream, kind, seq, message, result, events)
VALUES ('counter/a', 'counter', 1, '{"type":"add","v":1,"data":{}}', '1', '[{"type":"added","v":1,"data":{}}]');
INSERT INTO cairn.snapshots (stream, seq, version, state) VALUES ('counter/a', 1, 'v1', '{"Total": 1}');`); err != nil {
		t.Fatal(err)
	}

	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating a populated version 1 ledger: %v", err)
	}
	entries, err := store.ReadStream(ctx, Stream{Kind: "counter", ID: "a"}, 0)
	if err != nil {
		t.Fatalf("reading an entry written before the migration: %v", err)
	}
	if len(entries) != 1 || entries[0].Meta.Actor != "" || entries[0].Meta.Extra != nil || !entries[0].Meta.OccurredAt.IsZero() {
		t.Fatalf("an old entry should read back with empty metadata: %+v", entries)
	}

	// Snapshots move to the table keyed by version.
	var snapshot string
	if err := pool.QueryRow(ctx, `SELECT version || '@' || seq || ' ' || state::text FROM cairn.state_snapshots
WHERE stream = 'counter/a'`).Scan(&snapshot); err != nil {
		t.Fatalf("reading a snapshot taken before the migration: %v", err)
	}
	if snapshot != `v1@1 {"Total": 1}` {
		t.Fatalf("snapshot %q, want it carried over unchanged", snapshot)
	}
}
