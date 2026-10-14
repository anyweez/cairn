package ledger

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The library owns everything in the cairn schema. Applications read it
// through this package and never write to it directly: the guarantees below
// (append-only entries, refusals carry nothing, effects tied to the entry that
// produced them) hold only while that is true.
var migrations = []string{
	// 1: streams, the log, snapshots, the outbox and projection checkpoints.
	`
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
    UNIQUE (stream, seq),
    CONSTRAINT entries_refusal_inert
        CHECK (refusal IS NULL OR (events = '[]'::jsonb AND effects = '[]'::jsonb))
);
CREATE UNIQUE INDEX entries_idempotency ON cairn.entries (stream, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX entries_log ON cairn.entries (txid, position);
CREATE INDEX entries_kind_log ON cairn.entries (kind, txid, position);

CREATE FUNCTION cairn.refuse_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'cairn.entries is append-only';
END $$;
CREATE TRIGGER entries_append_only BEFORE UPDATE OR DELETE ON cairn.entries
    FOR EACH ROW EXECUTE FUNCTION cairn.refuse_change();
CREATE TRIGGER entries_no_truncate BEFORE TRUNCATE ON cairn.entries
    FOR EACH STATEMENT EXECUTE FUNCTION cairn.refuse_change();

-- A cache. Deleting every row is always safe; version names the state's shape,
-- and a snapshot of any other version is ignored.
CREATE TABLE cairn.snapshots (
    stream   text PRIMARY KEY REFERENCES cairn.streams (stream),
    seq      bigint NOT NULL,
    version  text NOT NULL,
    state    jsonb NOT NULL,
    taken_at timestamptz NOT NULL DEFAULT now()
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
`,
	// 2: who sent each message, through what, and why.
	`
ALTER TABLE cairn.entries
    ADD COLUMN actor          text,
    ADD COLUMN via            text,
    ADD COLUMN correlation_id text,
    ADD COLUMN causation_id   text,
    ADD COLUMN occurred_at    timestamptz,
    ADD COLUMN extra          jsonb NOT NULL DEFAULT '{}';
CREATE INDEX entries_actor ON cairn.entries (actor, position) WHERE actor IS NOT NULL;
CREATE INDEX entries_correlation ON cairn.entries (correlation_id, position) WHERE correlation_id IS NOT NULL;
`,
	// 3: snapshots keyed by version as well as stream. With one row per
	// stream, processes running different StateVersions during a rolling
	// deploy overwrite each other's snapshot, and each then folds the whole
	// stream and writes a fresh snapshot on every message. The new table sits
	// beside the old one rather than replacing its key, so processes still on
	// the old code keep writing cairn.snapshots instead of failing; a later
	// migration drops it.
	`
CREATE TABLE cairn.state_snapshots (
    stream   text NOT NULL REFERENCES cairn.streams (stream),
    version  text NOT NULL,
    seq      bigint NOT NULL,
    state    jsonb NOT NULL,
    taken_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (stream, version)
);
INSERT INTO cairn.state_snapshots (stream, version, seq, state, taken_at)
SELECT stream, version, seq, state, taken_at FROM cairn.snapshots;
`,
	// 4: take a transaction's stream locks and read their seqs in one
	// statement, so locking costs one round trip and can share a batch with
	// what follows it. The keys are the ones earlier code takes, so processes
	// on either side of a deploy still exclude each other.
	`
CREATE FUNCTION cairn.lock_streams(names text[])
RETURNS TABLE (locked_stream text, locked_seq bigint, locked_at timestamptz)
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
    -- not exist yet reads as seq 0. locked_at is the transaction's now(), the
    -- recorded_at of every entry it writes.
    RETURN QUERY
        SELECT n, coalesce(s.seq, 0), now()
        FROM unnest(names) AS n LEFT JOIN cairn.streams s ON s.stream = n;
END $$;
`,
}

// Migrate brings the cairn schema up to date. It is safe to call from every
// process at startup: concurrent callers serialize on an advisory lock.
func (s *Store) Migrate(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cairn.migrate'))`); err != nil {
			return fmt.Errorf("cairn: locking for migration: %w", err)
		}
		if _, err := tx.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS cairn;
CREATE TABLE IF NOT EXISTS cairn.schema_version (
    version    int PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
			return fmt.Errorf("cairn: creating schema: %w", err)
		}
		var current int
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM cairn.schema_version`).Scan(&current); err != nil {
			return fmt.Errorf("cairn: reading schema version: %w", err)
		}
		if current > len(migrations) {
			return fmt.Errorf("cairn: database schema is at version %d, newer than this build's %d", current, len(migrations))
		}
		for v := current + 1; v <= len(migrations); v++ {
			if _, err := tx.Exec(ctx, migrations[v-1]); err != nil {
				return fmt.Errorf("cairn: applying migration %d: %w", v, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO cairn.schema_version (version) VALUES ($1)`, v); err != nil {
				return fmt.Errorf("cairn: recording migration %d: %w", v, err)
			}
		}
		return nil
	})
}
