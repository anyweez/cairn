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

// Before migration 4 cairn.entries stored its value columns as jsonb, which
// parses a value and writes it back with object keys reordered and numbers
// normalised. The migration moves them to json, which keeps the text exactly.
// A row written by an earlier release must still read back, and a value
// written afterwards must be returned with the bytes it was given.
func TestEntryColumnsKeepTheirBytesAcrossTheMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	// Migrations 1 to 3 are the last schema whose entry columns are jsonb.
	if err := store.migrate(ctx, migrations[:3], migrationLeaseTTL); err != nil {
		t.Fatal(err)
	}

	// An entry as the previous release wrote it. jsonb had already reordered
	// and spaced its message, which is all that column could keep.
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.streams (stream, kind, seq) VALUES ('mig/a', 'mig', 1);
INSERT INTO cairn.entries (stream, kind, seq, message, result, events, effects, extra)
VALUES ('mig/a', 'mig', 1,
    '{"type":"mig.message","v":1,"data":{"n":1}}', NULL, '[]', '[]', '{}')`); err != nil {
		t.Fatal(err)
	}
	var oldMessage string
	if err := pool.QueryRow(ctx, `SELECT message::text FROM cairn.entries WHERE seq = 1`).Scan(&oldMessage); err != nil {
		t.Fatal(err)
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	old, err := store.ReadStream(ctx, Stream{Kind: "mig", ID: "a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) != 1 || old[0].Message.Type != "mig.message" {
		t.Fatalf("the entry written before the migration read back as %+v", old)
	}
	var migratedMessage string
	if err := pool.QueryRow(ctx, `SELECT message::text FROM cairn.entries WHERE seq = 1`).Scan(&migratedMessage); err != nil {
		t.Fatal(err)
	}
	if migratedMessage != oldMessage {
		t.Fatalf("the migration changed the old entry's text from %s to %s", oldMessage, migratedMessage)
	}

	// Every value column keeps the bytes it is given now, not a jsonb
	// re-serialisation of them.
	values := map[string]string{
		"message": `{"b":2,"a":1}`,
		"result":  `{"n":1e2}`,
		"events":  `[{"z":-0,"a":1}]`,
		"effects": `[{"n":1e2}]`,
		"extra":   `{"b":2,"a":1}`,
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cairn.streams (stream, kind, seq) VALUES ('mig/b', 'mig', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.entries (stream, kind, seq, message, result, events, effects, extra)
VALUES ('mig/b', 'mig', 1, $1, $2, $3, $4, $5)`,
		values["message"], values["result"], values["events"], values["effects"], values["extra"]); err != nil {
		t.Fatal(err)
	}
	for column, want := range values {
		var got string
		if err := pool.QueryRow(ctx, `SELECT `+column+`::text FROM cairn.entries WHERE stream = 'mig/b'`).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("the %s column returned %s, but was written %s", column, got, want)
		}
	}

	// The refusal constraint still holds against the column's new type.
	if _, err := pool.Exec(ctx, `
INSERT INTO cairn.entries (stream, kind, seq, message, refusal, events)
VALUES ('mig/b', 'mig', 2, '{}', '{}', '[{"type":"mig.event","v":1,"data":{}}]')`); err == nil {
		t.Fatal("a refusal carrying an event was recorded")
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
	if err := store.migrate(ctx, migs, migrationLeaseTTL); err != nil {
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
	if err := store.migrate(ctx, migs, migrationLeaseTTL); err == nil {
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
	if err := store.migrate(ctx, migs, migrationLeaseTTL); err == nil {
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
	if err := store.migrate(ctx, migs, migrationLeaseTTL); err == nil {
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
	go func() { first <- store.migrate(ctx, migs, migrationLeaseTTL) }()
	waitForMigrationLease(t, ctx, pool)

	second := make(chan error, 1)
	go func() { second <- store.migrate(ctx, migs, migrationLeaseTTL) }()
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
	go func() { done <- store.migrate(callerCtx, migs, migrationLeaseTTL) }()
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

// A migration can run far longer than the lease TTL: CREATE INDEX
// CONCURRENTLY over a large table is the routine case. The holder must renew
// its lease while it runs, or a second caller takes the lease from a live
// holder and reruns the same statements beside it.
func TestMigrationLeaseIsRenewedWhileHeld(t *testing.T) {
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
	const ttl = 500 * time.Millisecond

	// Hold the version table so the first caller stops after it takes the
	// lease and before it applies anything. It stays there for several TTLs.
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
	go func() { first <- store.migrate(ctx, migs, ttl) }()
	waitForMigrationLease(t, ctx, pool)
	holder, err := migrationLeaseHolder(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	second := make(chan error, 1)
	go func() { second <- store.migrate(ctx, migs, ttl) }()

	// Let several TTLs pass. Without the heartbeat the lease would expire,
	// the second caller would reclaim it, and the holder row would change
	// under the still-running first caller.
	time.Sleep(4 * ttl)
	if got, err := migrationLeaseHolder(ctx, pool); err != nil {
		t.Fatal(err)
	} else if got != holder {
		t.Fatalf("the lease moved from %s to %s after several TTLs; the heartbeat did not renew it", holder, got)
	}

	if err := gateTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan error{first, second} {
		if err := <-ch; err != nil {
			t.Errorf("a concurrent migrate returned %v", err)
		}
	}
}

// A caller whose lease is deleted or taken while it migrates no longer owns
// the schema. It must stop and must not record the version, or it would
// record a migration some other caller is applying.
func TestLostMigrationLeaseStopsTheMigration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	migs := append(slices.Clone(migrations), migration{
		Statements: []string{`CREATE TABLE cairn.migrate_probe (n int)`},
	})
	const ttl = 500 * time.Millisecond

	// Hold the version table so the caller stops after it takes the lease and
	// before it applies the migration.
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

	done := make(chan error, 1)
	go func() { done <- store.migrate(ctx, migs, ttl) }()
	waitForMigrationLease(t, ctx, pool)

	// The lease disappears under the holder, so the next beat updates no row
	// and the holder must cancel rather than record a version it lost.
	if _, err := pool.Exec(ctx, `DELETE FROM cairn.migration_lock WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a migrate whose lease was lost returned nil")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a migrate whose lease was lost did not return")
	}
	if err := gateTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM cairn.schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version %d after a lost lease, want %d", version, len(migrations))
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('cairn.migrate_probe') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("a migration whose lease was lost still applied")
	}
}

// A caller reads the schema version before it blocks on the migration's
// advisory lock. An earlier release's Migrate, or one whose lease expired
// mid-migration, can commit the same migration while it waits, so the caller
// must re-read the version under the lock and skip what is already applied
// instead of re-running it and failing on the second CREATE.
func TestBlockedCallerSkipsAnAlreadyAppliedMigration(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := emptyDatabase(t)
	store := New(pool, RequireActor(false))
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	migs := append(slices.Clone(migrations), migration{
		Statements: []string{`CREATE TABLE cairn.migrate_probe (n int)`},
	})
	version := len(migs)

	// The earlier caller holds only the advisory lock; it has not recorded
	// the version yet, so the caller under test reads the schema as it was
	// and then blocks behind the lock.
	older, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer older.Release()
	olderTx, err := older.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = olderTx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := olderTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairn.migrate'))`); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- store.migrate(ctx, migs, migrationLeaseTTL) }()
	waitForWaiterOnAdvisoryLock(t, ctx, pool)

	// The earlier caller now applies and commits the same migration while the
	// caller under test waits.
	if _, err := olderTx.Exec(ctx, `CREATE TABLE cairn.migrate_probe (n int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := olderTx.Exec(ctx, `INSERT INTO cairn.schema_version (version) VALUES ($1)`, version); err != nil {
		t.Fatal(err)
	}
	if err := olderTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a caller re-applied a migration that was already committed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a blocked migrate did not return")
	}
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM cairn.schema_version WHERE version = $1`, version).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Fatalf("migration %d recorded %d times, want 1", version, versions)
	}
}

// migrationLeaseHolder returns the holder id of the current migration lease.
func migrationLeaseHolder(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var holder string
	err := pool.QueryRow(ctx, `SELECT holder FROM cairn.migration_lock WHERE id = 1`).Scan(&holder)
	return holder, err
}

// waitForWaiterOnAdvisoryLock waits until a caller in this database is
// blocked on the migration's transaction-level advisory lock.
func waitForWaiterOnAdvisoryLock(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM pg_locks
WHERE locktype = 'advisory' AND NOT granted
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no migrate call blocked on the migration advisory lock")
		}
		time.Sleep(10 * time.Millisecond)
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
