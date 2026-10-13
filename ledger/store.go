// Package ledger is the Postgres shell around a cairn.Decider: it loads a
// stream's state, decides a message, and records the message, its decision,
// its events and its effects in one transaction.
//
// Every decided message is an entry, refused ones included, so the ledger is
// a complete record of what was asked and what was answered. That record is
// what the rest of the pattern stands on: state is rebuilt by folding stored
// events, the current code is verified by re-deciding stored messages, and
// effects are carried out from an outbox written in the same transaction.
package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
)

// Store is a ledger in one Postgres database.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a store over pool. Call Migrate before first use.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool returns the underlying pool, for projections that keep their read
// models in the same database.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Stream names one stream: a kind ("decision") and an ID within it ("42").
type Stream struct {
	Kind string
	ID   string
}

func (s Stream) String() string { return s.Kind + "/" + s.ID }

// Entry is one decided message as stored.
type Entry struct {
	Position       int64
	TxID           uint64
	Stream         string
	Kind           string
	Seq            int64
	Message        codec.Envelope
	Result         json.RawMessage
	Refusal        *cairn.Refusal
	Events         []codec.Envelope
	Effects        []codec.Envelope
	IdempotencyKey string
	RecordedAt     time.Time
}

// Cursor is a position in the global log.
type Cursor struct {
	TxID     uint64
	Position int64
}

// querier is what both a pool and a transaction offer.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Tx is a transaction that may write to the ledger. Streams are written only
// while locked, and all of a transaction's locks are taken in one Lock call.
type Tx struct {
	pgx.Tx
	locked map[string]int64
}

// ErrNotLocked is returned when a transaction writes a stream it did not lock.
var ErrNotLocked = errors.New("cairn: stream is not locked in this transaction")

// Transact runs fn in a transaction, committing if it returns nil.
func (s *Store) Transact(ctx context.Context, fn func(tx *Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(ptx pgx.Tx) error {
		return fn(&Tx{Tx: ptx})
	})
}

// Lock takes the write lock on every stream the transaction will touch,
// creating streams that do not exist yet. Locks are taken in a fixed order,
// which is why they must all be taken at once: two transactions that lock
// overlapping streams can then never deadlock. Lock may be called once, and
// must come before the transaction's first write.
//
// The global log is ordered by transaction ID, and Postgres assigns that ID at
// a transaction's first write. Every stream lock is therefore an advisory
// lock taken while the transaction has no ID yet: a transaction that queues
// behind another for a stream is assigned its ID after the other's, so its
// entries follow the other's in the log as they do in the stream.
func (tx *Tx) Lock(ctx context.Context, streams ...Stream) error {
	if tx.locked != nil {
		return errors.New("cairn: Lock may be called once per transaction; pass every stream to one call")
	}
	sorted := slices.Clone(streams)
	slices.SortFunc(sorted, func(a, b Stream) int { return strings.Compare(a.String(), b.String()) })
	sorted = slices.CompactFunc(sorted, func(a, b Stream) bool { return a == b })
	names := make([]string, len(sorted))
	for i, st := range sorted {
		if strings.Contains(st.Kind, "/") || st.Kind == "" || st.ID == "" {
			return fmt.Errorf("cairn: invalid stream %q", st)
		}
		names[i] = st.String()
	}

	var assigned bool
	if err := tx.QueryRow(ctx, `SELECT pg_current_xact_id_if_assigned() IS NOT NULL`).Scan(&assigned); err != nil {
		return fmt.Errorf("cairn: locking streams: %w", err)
	}
	if assigned {
		return errors.New("cairn: Lock must come before the transaction's first write")
	}
	// Ordered by lock key, not by name, so that two streams whose keys collide
	// are still taken in one global order.
	rows, err := tx.Query(ctx,
		`SELECT DISTINCT hashtextextended(s, 0) AS k FROM unnest($1::text[]) AS s ORDER BY k`, names)
	if err != nil {
		return fmt.Errorf("cairn: locking streams: %w", err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("cairn: locking streams: %w", err)
	}
	for _, k := range keys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, k); err != nil {
			return fmt.Errorf("cairn: locking streams: %w", err)
		}
	}

	tx.locked = make(map[string]int64, len(sorted))
	for _, st := range sorted {
		if _, err := tx.Exec(ctx,
			`INSERT INTO cairn.streams (stream, kind) VALUES ($1, $2) ON CONFLICT (stream) DO NOTHING`,
			st.String(), st.Kind); err != nil {
			return fmt.Errorf("cairn: creating stream %s: %w", st, err)
		}
		var seq int64
		if err := tx.QueryRow(ctx,
			`SELECT seq FROM cairn.streams WHERE stream = $1 FOR UPDATE`, st.String()).Scan(&seq); err != nil {
			return fmt.Errorf("cairn: locking stream %s: %w", st, err)
		}
		tx.locked[st.String()] = seq
	}
	return nil
}

func (tx *Tx) seq(stream Stream) (int64, error) {
	seq, ok := tx.locked[stream.String()]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotLocked, stream)
	}
	return seq, nil
}

// append writes one entry at the next sequence number of a locked stream.
func (tx *Tx) append(ctx context.Context, stream Stream, e Entry) (Entry, error) {
	seq, err := tx.seq(stream)
	if err != nil {
		return Entry{}, err
	}
	e.Stream, e.Kind, e.Seq = stream.String(), stream.Kind, seq+1

	message, err := json.Marshal(e.Message)
	if err != nil {
		return Entry{}, err
	}
	events, err := json.Marshal(nonNil(e.Events))
	if err != nil {
		return Entry{}, err
	}
	effects, err := json.Marshal(nonNil(e.Effects))
	if err != nil {
		return Entry{}, err
	}
	var refusal []byte
	if e.Refusal != nil {
		if refusal, err = json.Marshal(e.Refusal); err != nil {
			return Entry{}, err
		}
	}
	var key *string
	if e.IdempotencyKey != "" {
		key = &e.IdempotencyKey
	}

	var txid string
	err = tx.QueryRow(ctx, `
INSERT INTO cairn.entries (stream, kind, seq, message, result, refusal, events, effects, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING position, txid::text, recorded_at`,
		e.Stream, e.Kind, e.Seq, message, nullJSON(e.Result), refusal, events, effects, key,
	).Scan(&e.Position, &txid, &e.RecordedAt)
	if err != nil {
		return Entry{}, fmt.Errorf("cairn: appending to %s: %w", stream, err)
	}
	if e.TxID, err = strconv.ParseUint(txid, 10, 64); err != nil {
		return Entry{}, err
	}

	if _, err := tx.Exec(ctx,
		`UPDATE cairn.streams SET seq = $2, updated_at = now() WHERE stream = $1`, e.Stream, e.Seq); err != nil {
		return Entry{}, fmt.Errorf("cairn: advancing %s: %w", stream, err)
	}
	for i, effect := range e.Effects {
		data, err := json.Marshal(effect)
		if err != nil {
			return Entry{}, err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO cairn.outbox (stream, seq, idx, kind, effect) VALUES ($1, $2, $3, $4, $5)`,
			e.Stream, e.Seq, i, e.Kind, data); err != nil {
			return Entry{}, fmt.Errorf("cairn: queueing effect %d of %s#%d: %w", i, stream, e.Seq, err)
		}
	}
	tx.locked[e.Stream] = e.Seq
	return e, nil
}

func nonNil(envs []codec.Envelope) []codec.Envelope {
	if envs == nil {
		return []codec.Envelope{}
	}
	return envs
}

func nullJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

const entryColumns = `position, txid::text, stream, kind, seq, message, result, refusal, events, effects,
coalesce(idempotency_key, ''), recorded_at`

func scanEntries(rows pgx.Rows) ([]Entry, error) {
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e                       Entry
			txid                    string
			message, events, effect []byte
			result, refusal         []byte
		)
		if err := rows.Scan(&e.Position, &txid, &e.Stream, &e.Kind, &e.Seq, &message, &result, &refusal,
			&events, &effect, &e.IdempotencyKey, &e.RecordedAt); err != nil {
			return nil, err
		}
		var err error
		if e.TxID, err = strconv.ParseUint(txid, 10, 64); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(message, &e.Message); err != nil {
			return nil, fmt.Errorf("cairn: %s#%d message: %w", e.Stream, e.Seq, err)
		}
		if err := json.Unmarshal(events, &e.Events); err != nil {
			return nil, fmt.Errorf("cairn: %s#%d events: %w", e.Stream, e.Seq, err)
		}
		if err := json.Unmarshal(effect, &e.Effects); err != nil {
			return nil, fmt.Errorf("cairn: %s#%d effects: %w", e.Stream, e.Seq, err)
		}
		if refusal != nil {
			e.Refusal = &cairn.Refusal{}
			if err := json.Unmarshal(refusal, e.Refusal); err != nil {
				return nil, fmt.Errorf("cairn: %s#%d refusal: %w", e.Stream, e.Seq, err)
			}
		}
		e.Result = result
		out = append(out, e)
	}
	return out, rows.Err()
}

func readStream(ctx context.Context, q querier, stream Stream, afterSeq int64) ([]Entry, error) {
	rows, err := q.Query(ctx, `SELECT `+entryColumns+` FROM cairn.entries
WHERE stream = $1 AND seq > $2 ORDER BY seq`, stream.String(), afterSeq)
	if err != nil {
		return nil, fmt.Errorf("cairn: reading %s: %w", stream, err)
	}
	return scanEntries(rows)
}

// ReadStream returns a stream's entries after afterSeq, in order.
func (s *Store) ReadStream(ctx context.Context, stream Stream, afterSeq int64) ([]Entry, error) {
	return readStream(ctx, s.pool, stream, afterSeq)
}

func readAll(ctx context.Context, q querier, after Cursor, limit int, kinds []string) ([]Entry, error) {
	sql := `SELECT ` + entryColumns + ` FROM cairn.entries
WHERE (txid, position) > ($1::text::xid8, $2)
  AND txid < pg_snapshot_xmin(pg_current_snapshot())`
	args := []any{strconv.FormatUint(after.TxID, 10), after.Position}
	if len(kinds) > 0 {
		sql += ` AND kind = ANY($3)`
		args = append(args, kinds)
	}
	if limit <= 0 {
		limit = 100
	}
	sql += fmt.Sprintf(` ORDER BY txid, position LIMIT %d`, limit)
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("cairn: reading the log: %w", err)
	}
	return scanEntries(rows)
}

// ReadAll returns up to limit entries after the cursor, in transaction order
// (each stream's entries in seq order), optionally only of the given kinds. Entries from transactions still in
// flight are held back until every older transaction has finished, so a
// reader that advances its cursor past what it read never misses an entry.
func (s *Store) ReadAll(ctx context.Context, after Cursor, limit int, kinds ...string) ([]Entry, Cursor, error) {
	entries, err := readAll(ctx, s.pool, after, limit, kinds)
	if err != nil {
		return nil, after, err
	}
	if n := len(entries); n > 0 {
		after = Cursor{TxID: entries[n-1].TxID, Position: entries[n-1].Position}
	}
	return entries, after, nil
}

// Streams lists the IDs of every stream of a kind.
func (s *Store) Streams(ctx context.Context, kind string) ([]Stream, error) {
	rows, err := s.pool.Query(ctx, `SELECT stream FROM cairn.streams WHERE kind = $1 ORDER BY stream`, kind)
	if err != nil {
		return nil, err
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make([]Stream, 0, len(names))
	for _, n := range names {
		out = append(out, Stream{Kind: kind, ID: strings.TrimPrefix(n, kind+"/")})
	}
	return out, nil
}
