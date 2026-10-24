package ledger

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The heartbeat renews the migration lease on a pooled connection of its own.
// With a pool of one connection, the migrate caller already holds the only
// one, so the beat never runs and a migration that outlives its TTL silently
// loses the lease while its process is still alive.
func TestMigrationHeartbeatRenewsWithAPoolOfOne(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := emptyDatabase(t)
	if err := New(pool, RequireActor(false)).Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	limited, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()

	migs := append(slices.Clone(migrations), migration{
		Statements: []string{`CREATE TABLE cairn.migrate_probe (n int)`},
	})
	const ttl = 500 * time.Millisecond

	// Block the migration after it has taken the lease, on a connection from
	// the wide pool so the limited pool's only connection stays with migrate.
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

	store := New(limited, RequireActor(false))
	done := make(chan error, 1)
	go func() { done <- store.migrate(ctx, migs, ttl) }()
	waitForMigrationLease(t, ctx, pool)
	holder, err := migrationLeaseHolder(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(4 * ttl)
	var expired bool
	if err := pool.QueryRow(ctx,
		`SELECT expires_at <= now() FROM cairn.migration_lock WHERE id = 1 AND holder = $1`, holder).Scan(&expired); err != nil {
		t.Fatalf("reading the lease: %v", err)
	}
	if expired {
		t.Fatal("a live migration's lease expired: the heartbeat never ran, because migrate held the pool's only connection")
	}

	if err := gateTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the migration returned %v", err)
	}
}

// The heartbeat holds its connection for the whole migration, so a connection
// that dies -- a network reset, a pooler or server restart, a beat interrupted
// mid-statement -- would fail every later beat on the same dead handle and let
// the lease expire under a live holder. The heartbeat must renew on a
// replacement instead.
func TestMigrationHeartbeatReplacesADeadConnection(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := emptyDatabase(t)
	if err := New(pool, RequireActor(false)).Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	cfg := pool.Config()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	limited, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()

	migs := append(slices.Clone(migrations), migration{
		Statements: []string{`CREATE TABLE cairn.migrate_probe (n int)`},
	})
	const ttl = time.Second

	// Block the migration after it has taken the lease, as in the pool-of-one
	// test: the limited pool's only connection is the migration's, and the
	// heartbeat's is opened beside it.
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

	store := New(limited, RequireActor(false))
	done := make(chan error, 1)
	go func() { done <- store.migrate(ctx, migs, ttl) }()
	waitForMigrationLease(t, ctx, pool)
	holder, err := migrationLeaseHolder(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}

	// Kill the heartbeat's backend from a separate pool. The migration's own
	// backend is blocked on the gate, so the lease renewal is the only
	// statement this database sees from a heartbeat.
	pid := waitForHeartbeatBackend(t, ctx, pool)
	if _, err := pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatalf("terminating the heartbeat's backend: %v", err)
	}

	// Several TTLs later the heartbeat must have renewed on a replacement
	// connection; one that kept the dead handle would fail every beat and the
	// lease would expire under this still-live migration.
	time.Sleep(4 * ttl)
	var expired bool
	if err := pool.QueryRow(ctx,
		`SELECT expires_at <= now() FROM cairn.migration_lock WHERE id = 1 AND holder = $1`, holder).Scan(&expired); err != nil {
		t.Fatalf("reading the lease: %v", err)
	}
	if expired {
		t.Fatal("the lease expired after the heartbeat's connection was killed")
	}
	if got, err := migrationLeaseHolder(ctx, pool); err != nil {
		t.Fatal(err)
	} else if got != holder {
		t.Fatalf("the lease moved from %s to %s after its heartbeat's connection was killed", holder, got)
	}

	if err := gateTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the migration returned %v", err)
	}
}

// waitForHeartbeatBackend waits until a connection in this database has the
// lease renewal as its last statement, and returns its backend pid. The
// statement is passed as a parameter rather than written into this query, so
// this query's own text cannot match it once its backend goes idle.
func waitForHeartbeatBackend(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var pid int
		err := pool.QueryRow(ctx, `
SELECT pid FROM pg_stat_activity
WHERE datname = current_database()
  AND pid <> pg_backend_pid()
  AND query LIKE '%' || $1 || '%'`, `UPDATE cairn.migration_lock`).Scan(&pid)
		switch {
		case err == nil:
			return pid
		case !errors.Is(err, pgx.ErrNoRows):
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("no connection renewed the migration lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
