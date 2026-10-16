package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
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

// A concurrent migration is one whose statements cannot run inside a
// transaction. CREATE INDEX CONCURRENTLY is the reason the kind exists: it
// refuses any transaction block, so this passes only if the runner really
// sends each statement outside one.
func TestConcurrentMigrationRunsOutsideATransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	migs := append(slices.Clone(migrations), migration{
		Concurrent: true,
		Statements: []string{
			`CREATE INDEX CONCURRENTLY IF NOT EXISTS entries_kind_seq ON cairn.entries (kind, seq)`,
		},
	})
	if err := store.migrate(ctx, migs); err != nil {
		t.Fatalf("applying a concurrent migration: %v", err)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('cairn.entries_kind_seq') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("the concurrent migration's index was not created")
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version) FROM cairn.schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migs) {
		t.Fatalf("schema version %d after the concurrent migration, want %d", version, len(migs))
	}
}

// A transactional migration is still one unit: a statement that fails rolls
// back the ones before it and leaves the version unrecorded, so the next boot
// runs the migration again.
func TestTransactionalMigrationRollsBackOnFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	migs := append(slices.Clone(migrations), migration{
		Statements: []string{
			`CREATE TABLE cairn.migrate_probe (n int)`,
			`SELECT 1 / 0`,
		},
	})
	if err := store.migrate(ctx, migs); err == nil {
		t.Fatal("a failing migration reported no error")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('cairn.migrate_probe') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("a table from a rolled-back migration survived")
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version) FROM cairn.schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version %d after a failed migration, want %d", version, len(migrations))
	}
}
