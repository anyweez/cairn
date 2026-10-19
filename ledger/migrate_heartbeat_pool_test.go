package ledger

import (
	"context"
	"slices"
	"testing"
	"time"

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
