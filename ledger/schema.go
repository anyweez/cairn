package ledger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A migration is one step of the cairn schema. Statements run in order, and a
// released migration is never edited (see the comment on migrations below).
//
// A transactional migration, the usual kind, runs every statement and its
// version row in one transaction, so a failure leaves nothing behind. A
// concurrent migration cannot: DDL such as CREATE INDEX CONCURRENTLY refuses
// to run inside a transaction, so each of its statements is sent on its own
// and the version is recorded after the last. Its statements must be
// idempotent (IF NOT EXISTS) and safe to run again, because a failure partway
// through leaves the earlier ones applied with the version unrecorded.
//
// A failed or cancelled CREATE INDEX CONCURRENTLY leaves an INVALID index in
// place, and IF NOT EXISTS then treats it as done on the next attempt. Before
// recording a concurrent migration's version the runner fails if any index in
// the cairn schema is invalid, so a retry cannot record a version over a
// broken index: drop the invalid index with DROP INDEX CONCURRENTLY and
// migrate again.
type migration struct {
	Statements []string
	Concurrent bool
}

// The library owns everything in the cairn schema. Applications read it
// through this package and never write to it directly: the guarantees below
// (append-only entries, refusals carry nothing, effects tied to the entry that
// produced them) hold only while that is true.
//
// A migration that has been released is never edited or reordered: databases
// record how many have run, so a change would only reach a fresh one. Append
// the next one instead. Processes on an earlier release keep running against
// the newest schema during a deploy and after a rollback (see Migrate), so a
// migration never changes the shape of a table, column or function an earlier
// release reads; it adds alongside, and drops only once no supported release
// reads it. TestEveryReleasesStatementsRunOnTheNewestSchema holds the
// statements earlier releases send.
var migrations = []migration{
	// 1: streams, the log, snapshots, the outbox, projection checkpoints and
	// the lock functions.
	{Statements: []string{`
CREATE TABLE cairn.streams (
    stream     text PRIMARY KEY,
    kind       text NOT NULL,
    seq        bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX streams_kind ON cairn.streams (kind, stream);

-- One row per decided message, refused or not. txid orders the global log:
-- a reader consumes only rows whose transaction is older than every
-- transaction still running, so a row can never appear behind its cursor.
-- actor through extra say who sent the message, through what, and why.
CREATE TABLE cairn.entries (
    position        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    txid            xid8 NOT NULL DEFAULT pg_current_xact_id(),
    stream          text NOT NULL REFERENCES cairn.streams (stream),
    kind            text NOT NULL,
    seq             bigint NOT NULL,
    message         jsonb NOT NULL,
    result          jsonb,
    refusal         jsonb,
    events          jsonb NOT NULL DEFAULT '[]',
    effects         jsonb NOT NULL DEFAULT '[]',
    idempotency_key text,
    recorded_at     timestamptz NOT NULL DEFAULT now(),
    actor           text,
    via             text,
    correlation_id  text,
    causation_id    text,
    occurred_at     timestamptz,
    extra           jsonb NOT NULL DEFAULT '{}',
    UNIQUE (stream, seq),
    CONSTRAINT entries_refusal_inert
        CHECK (refusal IS NULL OR (events = '[]'::jsonb AND effects = '[]'::jsonb))
);
CREATE UNIQUE INDEX entries_idempotency ON cairn.entries (stream, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX entries_log ON cairn.entries (txid, position);
CREATE INDEX entries_kind_log ON cairn.entries (kind, txid, position);
CREATE INDEX entries_actor ON cairn.entries (actor, position) WHERE actor IS NOT NULL;
CREATE INDEX entries_correlation ON cairn.entries (correlation_id, position) WHERE correlation_id IS NOT NULL;

CREATE FUNCTION cairn.refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'cairn.entries is append-only';
END $$;
CREATE TRIGGER entries_append_only BEFORE UPDATE OR DELETE ON cairn.entries
    FOR EACH ROW EXECUTE FUNCTION cairn.refuse_change();
CREATE TRIGGER entries_no_truncate BEFORE TRUNCATE ON cairn.entries
    FOR EACH STATEMENT EXECUTE FUNCTION cairn.refuse_change();

-- A cache. Deleting every row is always safe; version names the state's shape,
-- and each version keeps its own snapshot, so processes running different
-- versions side by side don't evict each other's.
CREATE TABLE cairn.state_snapshots (
    stream   text NOT NULL REFERENCES cairn.streams (stream),
    version  text NOT NULL,
    seq      bigint NOT NULL,
    state    jsonb NOT NULL,
    taken_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stream, version)
);

CREATE TABLE cairn.outbox (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stream       text NOT NULL,
    seq          bigint NOT NULL,
    idx          int NOT NULL,
    kind         text NOT NULL,
    effect       jsonb NOT NULL,
    attempts     int NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    done_at      timestamptz,
    failed_at    timestamptz,
    last_error   text,
    UNIQUE (stream, seq, idx),
    FOREIGN KEY (stream, seq) REFERENCES cairn.entries (stream, seq)
);
CREATE INDEX outbox_pending ON cairn.outbox (kind, id)
    WHERE done_at IS NULL AND failed_at IS NULL;

CREATE TABLE cairn.checkpoints (
    name       text PRIMARY KEY,
    txid       xid8 NOT NULL DEFAULT '0',
    position   bigint NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Take a transaction's stream locks and read their seqs in one statement, so
-- locking costs one round trip and can share a batch with what follows it.
-- The lock key is each stream name's hashtextextended; every release must
-- take the same keys, so processes on different releases exclude each other.
CREATE FUNCTION cairn.lock_streams(names text[])
RETURNS TABLE (locked_stream text, locked_seq bigint)
LANGUAGE plpgsql AS $$
DECLARE
    k bigint;
BEGIN
    -- The global log is ordered by transaction ID, assigned at the first
    -- write; a transaction that waited for a stream must get its ID after
    -- the one it waited for.
    IF pg_current_xact_id_if_assigned() IS NOT NULL THEN
        RAISE EXCEPTION 'Lock must come before the transaction''s first write'
            USING ERRCODE = 'CN001';
    END IF;
    -- Ordered by key, not by name, so that two streams whose keys collide are
    -- still taken in one global order.
    FOR k IN SELECT DISTINCT hashtextextended(n, 0) FROM unnest(names) AS n ORDER BY 1 LOOP
        PERFORM pg_advisory_xact_lock(k);
    END LOOP;
    -- A fresh snapshot, taken now that every lock is held. A stream that does
    -- not exist yet reads as seq 0.
    RETURN QUERY
        SELECT n, coalesce(s.seq, 0)
        FROM unnest(names) AS n LEFT JOIN cairn.streams s ON s.stream = n;
END $$;

-- Lock one stream on the condition that it is still at the seq the caller
-- last saw, so a process can decide from state it holds in memory and record
-- the decision in the same round trip as the lock.
CREATE FUNCTION cairn.lock_stream_at(name text, expected bigint) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    current bigint;
BEGIN
    IF pg_current_xact_id_if_assigned() IS NOT NULL THEN
        RAISE EXCEPTION 'Lock must come before the transaction''s first write'
            USING ERRCODE = 'CN001';
    END IF;
    PERFORM pg_advisory_xact_lock(hashtextextended(name, 0));
    SELECT seq INTO current FROM cairn.streams WHERE stream = name;
    IF coalesce(current, 0) <> expected THEN
        RAISE EXCEPTION 'stream % is at seq %, not %', name, coalesce(current, 0), expected
            USING ERRCODE = 'CN002';
    END IF;
END $$;
`}},

	// 2: a snapshot keeps the bytes json.Marshal wrote. jsonb parses a state and
	// re-serialises it, reordering object keys, reformatting numbers and
	// normalising whitespace, so a state loaded from a snapshot could differ
	// from the one that was stored. json stores the text as written and returns
	// it unchanged; every reader and writer of this column already treats it as
	// text, so a process on an earlier release keeps working across the change
	// and after a rollback.
	{Statements: []string{`
ALTER TABLE cairn.state_snapshots ALTER COLUMN state TYPE json USING state::json`}},

	// 3: the outbox is a queue and no longer keeps delivered effects: a
	// dispatcher deletes an effect's row as it records the delivery, so the
	// table holds only work outstanding or failed rather than every effect ever
	// delivered. This clears what earlier releases left behind, which nothing
	// reads once a row is done. Rows marked failed stay: an operator clears
	// those. A dispatcher still treats a done row as finished, because a
	// rolling deploy runs older and newer releases side by side.
	{Statements: []string{`
DELETE FROM cairn.outbox WHERE done_at IS NOT NULL`}},
}

// Migrate brings the cairn schema up to date. It is safe to call from every
// process at startup: concurrent callers serialize on a lease row in the
// cairn schema, which every step of the protocol reads or writes with one
// committed statement, so it excludes other callers through a transaction-
// mode connection pooler as well as on a direct connection.
//
// A database a later release has already migrated is left as it is, so an
// earlier build still starts after a rollback: migrations only add, and never
// change what an earlier release reads.
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrate(ctx, migrations, migrationLeaseTTL)
}

// migrationLeaseTTL is how long a migrate caller's lease lasts without a
// renewal, so it bounds how long a process that dies mid-migration blocks
// every other caller. A live caller renews its lease while it migrates,
// however long that takes (see startMigrationHeartbeat), and a caller that
// returns, fails or is cancelled deletes its own row first, so the TTL is
// only ever reached by a process that died.
const migrationLeaseTTL = 5 * time.Minute

// migrationLockPoll is how long migrate waits between attempts to take the
// migration lease.
const migrationLockPoll = 50 * time.Millisecond

// migrate applies migs, the released migrations in order. The whole call is
// serialized by a committed lease row rather than a session-level advisory
// lock: a transaction-mode pooler may put each statement of a call on a
// different server connection, where a session lock taken on one connection
// excludes nobody, and a poll that lands on the connection already holding it
// succeeds because session advisory locks are re-entrant. Every step here is
// one autocommitted statement, so the committed lease is visible to every
// caller through the pooler, and a waiter holds no snapshot while it polls.
// The holder renews the lease while it migrates, so a build that outlasts the
// TTL does not lose it to a waiter.
func (s *Store) migrate(ctx context.Context, migs []migration, ttl time.Duration) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("cairn: acquiring a connection to migrate: %w", err)
	}
	if err := ensureMigrationLock(ctx, conn); err != nil {
		conn.Release()
		return err
	}
	holder, err := acquireMigrationLease(ctx, conn, ttl)
	if err != nil {
		conn.Release()
		return err
	}
	// A migration can outlast its lease, so renew it while it runs: a lease
	// that expires under a live holder lets a second caller take it and rerun
	// the same statements alongside. The heartbeat cancels migrateCtx when it
	// can no longer renew, so the migration's next statement fails.
	migrateCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeat := startMigrationHeartbeat(migrateCtx, s.pool, holder, ttl, cancel)
	err = applyMigrations(migrateCtx, conn, holder, migs)
	// Stop the heartbeat before releasing the lease, so no beat renews a row
	// this caller is about to delete and a lost lease is reported.
	if heartbeatErr := heartbeat.stop(); heartbeatErr != nil {
		err = heartbeatErr
	}
	// Release the lease on a context that outlives the caller's, even when the
	// caller was cancelled: a lease left behind is only reclaimable when it
	// expires. A cancellation often closes the connection the statement was
	// sent on, so a failed DELETE is retried on a fresh one.
	releaseCtx := context.WithoutCancel(ctx)
	_, relErr := conn.Exec(releaseCtx,
		`DELETE FROM cairn.migration_lock WHERE id = 1 AND holder = $1`, holder)
	conn.Release()
	if relErr != nil {
		if fresh, aerr := s.pool.Acquire(releaseCtx); aerr == nil {
			_, _ = fresh.Exec(releaseCtx,
				`DELETE FROM cairn.migration_lock WHERE id = 1 AND holder = $1`, holder)
			fresh.Release()
		}
	}
	return err
}

// applyMigrations creates the version table and runs every migration after the
// recorded version. The caller holds the migration lease.
func applyMigrations(ctx context.Context, conn *pgxpool.Conn, holder string, migs []migration) error {
	if _, err := conn.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS cairn;
CREATE TABLE IF NOT EXISTS cairn.schema_version (
    version    int PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("cairn: creating schema: %w", err)
	}
	var current int
	if err := conn.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM cairn.schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("cairn: reading schema version: %w", err)
	}
	for v := current + 1; v <= len(migs); v++ {
		if err := applyMigration(ctx, conn, holder, v, migs[v-1]); err != nil {
			return err
		}
	}
	return nil
}

// ensureMigrationLock creates the cairn schema and the migration lease's
// table if they do not exist. Callers run it concurrently on a fresh
// database, and CREATE ... IF NOT EXISTS races: each caller checks the catalog
// before the other has committed, both insert the name, and the loser gets a
// unique violation instead of the no-op IF NOT EXISTS promises. The statement
// is idempotent, so the loser retries until it observes what the winner
// committed. Retrying rather than serializing keeps the wait to its own
// statement, so no caller holds a snapshot while another bootstraps.
func ensureMigrationLock(ctx context.Context, conn *pgxpool.Conn) error {
	for {
		_, err := conn.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS cairn;
CREATE TABLE IF NOT EXISTS cairn.migration_lock (
    id         int PRIMARY KEY,
    holder     text NOT NULL,
    expires_at timestamptz NOT NULL
)`)
		if err == nil {
			return nil
		}
		if !isConcurrentCreate(err) {
			return fmt.Errorf("cairn: creating the migration lock: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cairn: creating the migration lock: %w", ctx.Err())
		case <-time.After(migrationLockPoll):
		}
	}
}

// isConcurrentCreate reports whether err is the unique violation two callers
// racing to CREATE the same schema or table produce. Only that race is
// retried: any other error, a duplicate table over a relation that is not a
// table included, would fail the same way again.
func isConcurrentCreate(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" // unique_violation on pg_namespace or pg_type
}

// acquireMigrationLease takes the migration lease and returns this caller's
// holder id. Each attempt is one autocommitted statement, so a transaction-
// mode pooler can send it on any connection and the committed row still
// excludes every other caller. An unexpired lease is left alone and the
// attempt returns no row; a caller whose context ends while waiting returns an
// error, and the lease it never took is untouched.
func acquireMigrationLease(ctx context.Context, conn *pgxpool.Conn, ttl time.Duration) (string, error) {
	holder, err := newMigrationHolder()
	if err != nil {
		return "", err
	}
	for {
		var taken string
		err := conn.QueryRow(ctx, `
INSERT INTO cairn.migration_lock (id, holder, expires_at)
VALUES (1, $1, now() + make_interval(secs => $2))
ON CONFLICT (id) DO UPDATE
    SET holder = excluded.holder, expires_at = excluded.expires_at
    WHERE cairn.migration_lock.expires_at <= now()
RETURNING holder`, holder, ttl.Seconds()).Scan(&taken)
		switch {
		case err == nil:
			return taken, nil
		case errors.Is(err, pgx.ErrNoRows):
			// Another caller holds a lease that has not expired.
		default:
			return "", fmt.Errorf("cairn: taking the migration lease: %w", err)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("cairn: waiting for the migration lease: %w", ctx.Err())
		case <-time.After(migrationLockPoll):
		}
	}
}

// newMigrationHolder returns an id that identifies one migrate caller's lease.
func newMigrationHolder() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("cairn: generating a migration holder id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// migrationHeartbeat renews the migration lease while its holder migrates.
// Each beat is one autocommitted statement on its own pooled connection, so
// it stays visible through a transaction-mode pooler, and a connection is
// acquired only for the beat: a migration that finishes before the first tick
// never costs a second connection.
//
// A beat that updates no row means the lease is no longer this caller's,
// which happens when it expired and another caller reclaimed it. The
// heartbeat then cancels the migration's context so its next statement fails,
// and migrate returns the lost-lease error rather than a bare cancellation.
type migrationHeartbeat struct {
	stopCh chan struct{}
	doneCh chan struct{}
	cancel context.CancelFunc
	mu     sync.Mutex
	lost   error
}

// startMigrationHeartbeat starts the renewal loop for holder's lease. cancel
// is called if the lease is lost, so the migration the heartbeat guards stops
// before it can record anything.
func startMigrationHeartbeat(ctx context.Context, pool *pgxpool.Pool, holder string, ttl time.Duration, cancel context.CancelFunc) *migrationHeartbeat {
	// The heartbeat's own context is separate from the migration's, so stop
	// can unblock a beat that is waiting on the database; losing the lease
	// cancels the migration through cancel instead.
	hbCtx, hbCancel := context.WithCancel(context.WithoutCancel(ctx))
	h := &migrationHeartbeat{stopCh: make(chan struct{}), doneCh: make(chan struct{}), cancel: hbCancel}
	go func() {
		defer close(h.doneCh)
		// Renew well inside the TTL, so one missed beat does not let the lease
		// expire before the next.
		ticker := time.NewTicker(ttl / 3)
		defer ticker.Stop()
		for {
			select {
			case <-h.stopCh:
				return
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				tag, err := pool.Exec(hbCtx, `
UPDATE cairn.migration_lock
SET expires_at = now() + make_interval(secs => $2)
WHERE id = 1 AND holder = $1`, holder, ttl.Seconds())
				if err != nil {
					// A transient failure leaves the row alone; the next beat
					// retries. Only a beat that succeeds and updates no row
					// proves the lease is not ours.
					continue
				}
				if tag.RowsAffected() == 0 {
					h.mu.Lock()
					h.lost = fmt.Errorf("cairn: the migration lease held by %s was lost to another caller", holder)
					h.mu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	return h
}

// stop cancels the heartbeat, waits for it to finish, and returns the
// lease-loss error if one was recorded. It must run before the lease is
// released, so no beat outlives this caller's hold on it.
func (h *migrationHeartbeat) stop() error {
	close(h.stopCh)
	h.cancel()
	<-h.doneCh
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lost
}

// applyMigration runs one migration and records the version it reached. It
// returns nil without running anything when the version is already recorded,
// which a caller that read the version before blocking for the migration lock
// sees after it re-reads it under the lock.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, holder string, version int, m migration) error {
	if m.Concurrent {
		for i, stmt := range m.Statements {
			if _, err := conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("cairn: applying migration %d statement %d: %w", version, i+1, err)
			}
		}
		invalid, err := invalidIndexes(ctx, conn)
		if err != nil {
			return fmt.Errorf("cairn: checking migration %d for invalid indexes: %w", version, err)
		}
		if len(invalid) > 0 {
			return fmt.Errorf("cairn: migration %d left invalid indexes %s; drop each with DROP INDEX CONCURRENTLY and migrate again", version, strings.Join(invalid, ", "))
		}
		if err := requireMigrationLease(ctx, conn, holder, version); err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, `INSERT INTO cairn.schema_version (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("cairn: recording migration %d: %w", version, err)
		}
		return nil
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// The advisory lock excludes an older release's Migrate, which takes
		// this same lock but not the lease row. It does not exclude a caller
		// that has already read the schema version and is waiting here: that
		// caller re-reads the version below and skips a migration already
		// recorded. The lock is held for this transaction, which pins one
		// server connection, so it works through a transaction-mode pooler.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairn.migrate'))`); err != nil {
			return fmt.Errorf("cairn: locking for migration %d: %w", version, err)
		}
		var applied int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM cairn.schema_version`).Scan(&applied); err != nil {
			return fmt.Errorf("cairn: re-reading schema version for migration %d: %w", version, err)
		}
		if applied >= version {
			return nil
		}
		for i, stmt := range m.Statements {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("cairn: applying migration %d statement %d: %w", version, i+1, err)
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO cairn.schema_version (version) VALUES ($1)`, version); err != nil {
			return fmt.Errorf("cairn: recording migration %d: %w", version, err)
		}
		return nil
	})
}

// requireMigrationLease fails unless holder still owns the migration lease.
// A concurrent migration runs its statements outside any transaction, so
// nothing else stops a caller whose lease was taken from recording a version
// it no longer owns.
func requireMigrationLease(ctx context.Context, conn *pgxpool.Conn, holder string, version int) error {
	var held int
	err := conn.QueryRow(ctx,
		`SELECT 1 FROM cairn.migration_lock WHERE id = 1 AND holder = $1`, holder).Scan(&held)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("cairn: migration %d: the migration lease held by %s was lost before recording", version, holder)
	case err != nil:
		return fmt.Errorf("cairn: checking the migration lease before recording migration %d: %w", version, err)
	}
	return nil
}

// invalidIndexes names every index in the cairn schema that is not valid. A
// failed or cancelled CREATE INDEX CONCURRENTLY leaves one behind, and IF NOT
// EXISTS treats it as done on the next attempt, so the runner refuses to
// record a version while one exists. cairn owns the whole schema and a
// migration never creates an index outside it, so any invalid index here is
// one a concurrent migration left.
func invalidIndexes(ctx context.Context, conn *pgxpool.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `
SELECT c.relname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE NOT i.indisvalid AND n.nspname = 'cairn'
ORDER BY c.relname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}
