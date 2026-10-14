package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// emptyDatabase creates a database with nothing in it, dropped when the test
// ends. Tests inside the package use it, since ledgertest imports ledger.
func emptyDatabase(t *testing.T) *pgxpool.Pool {
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
		t.Fatal(err)
	}
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "cairn_internal_" + hex.EncodeToString(suffix)
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
	return pool
}

// After a rollback, an earlier build migrates a database a later release has
// already migrated. It must leave the schema alone and start.
func TestMigrateLeavesANewerSchemaAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	newer := len(migrations) + 1
	if _, err := pool.Exec(ctx, `INSERT INTO cairn.schema_version (version) VALUES ($1)`, newer); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating a database a later release migrated: %v", err)
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version) FROM cairn.schema_version`).Scan(&version); err != nil || version != newer {
		t.Fatalf("schema version %d, %v; want %d left as it was", version, err, newer)
	}
}

// During a deploy, and after a rollback, processes on an earlier release run
// their own statements against the newest schema. Every statement a release
// sends against a cairn function must still work once every migration has run;
// when a release changes one, add the new statement and keep the old one here.
func TestEveryReleasesStatementsRunOnTheNewestSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	if err := New(pool, RequireActor(false)).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []struct {
		release, sql string
		args         []any
	}{
		{"v0.1.0", `SELECT locked_stream, locked_seq FROM cairn.lock_streams($1)`, []any{[]string{"counter/a"}}},
		{"v0.1.0", `SELECT cairn.lock_stream_at($1, $2)`, []any{"counter/a", int64(0)}},
	} {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, stmt.sql, stmt.args...)
		if err == nil {
			_, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (any, error) { return r.Values() })
		}
		_ = tx.Rollback(ctx)
		if err != nil {
			t.Errorf("%s's %q fails on the newest schema: %v", stmt.release, stmt.sql, err)
		}
	}
}

// Postgres assigns a transaction its ID at its first write, and the global log
// is ordered by that ID, so a transaction that wrote before taking its locks
// could land in the log ahead of one it queued behind.
func TestLockRefusesATransactionThatHasWritten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := New(emptyDatabase(t), RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Transact locks before anything else, so the guard is reached only by
	// sending the lock statement by hand after a write.
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		// Assigns the transaction its ID, as any write would.
		if _, err := tx.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
			return err
		}
		var b Batch
		if _, err := queueLock(&b, []Stream{{Kind: "counter", ID: "a"}}); err != nil {
			return err
		}
		return b.send(ctx, tx)
	})
	if !errors.Is(err, errLockAfterWrite) {
		t.Fatalf("lock after a write: %v, want errLockAfterWrite", err)
	}
}
