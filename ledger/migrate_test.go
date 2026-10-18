package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

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

// A failed CREATE INDEX CONCURRENTLY leaves an INVALID index in place, and
// IF NOT EXISTS then treats it as done. Recording the version would tell the
// next boot that a broken index was built, so the runner must refuse.
func TestConcurrentMigrationDoesNotRecordOverAnInvalidIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Two streams of one kind, so a unique index over kind cannot build.
	if _, err := pool.Exec(ctx, `INSERT INTO cairn.streams (stream, kind) VALUES ('a', 'counter'), ('b', 'counter')`); err != nil {
		t.Fatal(err)
	}
	migs := append(slices.Clone(migrations), migration{
		Concurrent: true,
		Statements: []string{
			`CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS streams_kind_unique ON cairn.streams (kind)`,
		},
	})
	if err := store.migrate(ctx, migs); err == nil {
		t.Fatal("a concurrent migration over duplicate rows reported no error")
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = 'cairn.streams_kind_unique'::regclass`).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if valid {
		t.Fatal("the failed build left a valid index; the trap was not exercised")
	}
	// The retry finds the index present and would record the version over it
	// if the runner did not check indisvalid first.
	if err := store.migrate(ctx, migs); err == nil {
		t.Fatal("a retry recorded a version over an invalid index")
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT max(version) FROM cairn.schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version %d after a retry over an invalid index, want %d", version, len(migrations))
	}
}

// A process waiting for the migration lock must not hold a snapshot while it
// waits. CREATE INDEX CONCURRENTLY waits for older snapshots to end, so a
// waiter that held one would deadlock against the build it is waiting for:
// the build waits on the waiter's snapshot, and the waiter waits on the
// build's session lock. Both callers must return nil.
func TestConcurrentBootsDoNotDeadlockOnTheMigrationLock(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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

	// Hold the version table so the first migrate stops after it takes the
	// migration lock and before it reaches the index build. The second
	// caller's lock attempt then happens before the build takes its snapshot,
	// so the waiter's snapshot is older than the build's: the deadlock.
	gate, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Release()
	gateTx, err := gate.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gateTx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := gateTx.Exec(ctx, `LOCK TABLE cairn.schema_version IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	go func() { first <- store.migrate(ctx, migs) }()
	waitForMigrationLease(t, ctx, pool)

	second := make(chan error, 1)
	go func() { second <- store.migrate(ctx, migs) }()
	// Let the second caller reach the lease and, with a blocking lock, take its
	// snapshot. With the polling lease it holds none, so the build never waits.
	time.Sleep(300 * time.Millisecond)

	if err := gateTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan error{first, second} {
		if err := <-ch; err != nil {
			t.Errorf("a concurrent migrate returned %v", err)
		}
	}
}

// A caller that is cancelled while it holds the migration lease still
// releases it: the release runs on a context that outlives the cancellation,
// because the cancelled context can no longer send the DELETE. The next boot
// then takes the lease instead of waiting out its expiry.
func TestCancelledMigrateReleasesTheMigrationLease(t *testing.T) {
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

	// Hold the version table so the caller stops after it takes the lease and
	// before it applies anything.
	gate, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Release()
	gateTx, err := gate.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateTx.Exec(ctx, `LOCK TABLE cairn.schema_version IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	callerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- store.migrate(callerCtx, migs) }()
	waitForMigrationLease(t, ctx, pool)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled migrate returned nil")
	}
	if err := gateTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var held int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cairn.migration_lock WHERE id = 1`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("a cancelled caller left %d lease rows behind", held)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating after a cancelled caller: %v", err)
	}
}

// A caller that dies holding the lease leaves a row nothing deletes, so the
// lease must be reclaimable once it expires; otherwise every later boot would
// wait for a process that is gone.
func TestExpiredMigrationLeaseIsReclaimable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.migration_lock (id, holder, expires_at)
VALUES (1, 'dead-process', now() - interval '1 minute')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrating over an expired lease: %v", err)
	}
	var held int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cairn.migration_lock WHERE id = 1`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("the reclaimed lease was not released: %d rows", held)
	}
}

// waitForMigrationLease waits until a migrate call holds the migration lease.
// The lease is a committed row, so this pool sees the holder's row even
// though the holder is a different connection.
func waitForMigrationLease(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var held int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM cairn.migration_lock WHERE id = 1`).Scan(&held); err != nil {
			t.Fatal(err)
		}
		if held > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no migrate call took the migration lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
