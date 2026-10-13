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
	pool         *pgxpool.Pool
	requireActor bool
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// RequireActor says whether every message must have an actor in its metadata.
// It is on by default, so every entry is attributed; tests turn it off.
func RequireActor(required bool) StoreOption { return func(s *Store) { s.requireActor = required } }

// New returns a store over pool. Call Migrate before first use.
func New(pool *pgxpool.Pool, opts ...StoreOption) *Store {
	s := &Store{pool: pool, requireActor: true}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

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
	Meta           Meta
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
	locks *locks
}

// locks is what a transaction's Lock found: the seq of each stream it locked,
// advanced as entries are queued.
type locks struct {
	seqs map[string]int64
}

// recordingTime is the recorded_at of entries recorded now. It comes from
// this process, not the database, so that it is known before the entry is
// sent, and is cut to the microseconds Postgres keeps.
func recordingTime() time.Time { return time.Now().Truncate(time.Microsecond) }

// ErrNotLocked is returned when a transaction writes a stream it did not lock.
var ErrNotLocked = errors.New("cairn: stream is not locked in this transaction")

// Transact runs fn in a transaction, committing if it returns nil.
func (s *Store) Transact(ctx context.Context, fn func(tx *Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(ptx pgx.Tx) error {
		return fn(&Tx{Tx: ptx})
	})
}

// Lock takes the write lock on every stream the transaction will touch.
// Locking a stream that does not exist yet does not create it: its row is
// written with its first entry, so a stream locked only to be read leaves
// nothing behind. Locks are taken in a fixed order,
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
	if tx.locks != nil {
		return errors.New("cairn: Lock may be called once per transaction; pass every stream to one call")
	}
	var b Batch
	l, err := queueLock(&b, streams)
	if err != nil {
		return err
	}
	if err := b.send(ctx, tx.Tx); err != nil {
		return err
	}
	tx.locks = l
	return nil
}

// errLockAfterWrite is cairn.lock_streams refusing a transaction that has
// already written.
var errLockAfterWrite = errors.New("cairn: Lock must come before the transaction's first write")

// queueLock queues taking every stream's lock and reading its seq, in one
// statement. The returned locks are filled in when the batch runs.
func queueLock(b *Batch, streams []Stream) (*locks, error) {
	sorted := slices.Clone(streams)
	slices.SortFunc(sorted, func(a, b Stream) int { return strings.Compare(a.String(), b.String()) })
	sorted = slices.CompactFunc(sorted, func(a, b Stream) bool { return a == b })
	names := make([]string, len(sorted))
	for i, st := range sorted {
		if strings.Contains(st.Kind, "/") || st.Kind == "" || st.ID == "" {
			return nil, fmt.Errorf("cairn: invalid stream %q", st)
		}
		names[i] = st.String()
	}

	l := &locks{seqs: make(map[string]int64, len(names))}
	b.own(func(r pgx.BatchResults) error {
		rows, err := r.Query()
		if err == nil {
			var (
				name string
				seq  int64
			)
			_, err = pgx.ForEachRow(rows, []any{&name, &seq}, func() error {
				l.seqs[name] = seq
				return nil
			})
		}
		return lockError(err)
	}, `SELECT locked_stream, locked_seq FROM cairn.lock_streams($1)`, names)
	return l, nil
}

// errStale is cairn.lock_stream_at finding the stream moved on from the seq
// its caller decided against.
var errStale = errors.New("cairn: the stream has moved on")

// queueLockAt queues taking one stream's lock on the condition that it is
// still at seq, failing the batch with errStale otherwise.
func queueLockAt(b *Batch, stream Stream, seq int64) *locks {
	b.own(func(r pgx.BatchResults) error {
		_, err := r.Exec()
		return lockError(err)
	}, `SELECT cairn.lock_stream_at($1, $2)`, stream.String(), seq)
	return &locks{seqs: map[string]int64{stream.String(): seq}}
}

func lockError(err error) error {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pgErr) && pgErr.Code == "CN001":
		return errLockAfterWrite
	case errors.As(err, &pgErr) && pgErr.Code == "CN002":
		return fmt.Errorf("%w: %w", errStale, err)
	}
	return fmt.Errorf("cairn: locking streams: %w", err)
}

func (l *locks) seq(stream Stream) (int64, error) {
	var (
		seq int64
		ok  bool
	)
	if l != nil {
		seq, ok = l.seqs[stream.String()]
	}
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrNotLocked, stream)
	}
	return seq, nil
}

// queueAppend queues writing e at the next seq of a locked stream, recorded
// at at, advancing the stream in l. e is filled in as far as it can be now;
// its Position and TxID are filled in when the batch runs.
func (l *locks) queueAppend(b *Batch, stream Stream, e *Entry, at time.Time) error {
	seq, err := l.seq(stream)
	if err != nil {
		return err
	}
	e.Stream, e.Kind, e.Seq, e.RecordedAt = stream.String(), stream.Kind, seq+1, at

	message, err := json.Marshal(e.Message)
	if err != nil {
		return err
	}
	events, err := json.Marshal(nonNil(e.Events))
	if err != nil {
		return err
	}
	effects, err := json.Marshal(nonNil(e.Effects))
	if err != nil {
		return err
	}
	var refusal []byte
	if e.Refusal != nil {
		if refusal, err = json.Marshal(e.Refusal); err != nil {
			return err
		}
	}
	var key *string
	if e.IdempotencyKey != "" {
		key = &e.IdempotencyKey
	}
	extra := []byte(`{}`)
	if len(e.Meta.Extra) > 0 {
		if extra, err = json.Marshal(e.Meta.Extra); err != nil {
			return fmt.Errorf("cairn: encoding metadata for %s: %w", stream, err)
		}
	}
	var occurred *time.Time
	if !e.Meta.OccurredAt.IsZero() {
		occurred = &e.Meta.OccurredAt
	}
	outbox := make([]string, len(e.Effects))
	for i, effect := range e.Effects {
		data, err := json.Marshal(effect)
		if err != nil {
			return err
		}
		outbox[i] = string(data)
	}

	// The first entry creates the stream; Lock only reserved it.
	b.own(func(r pgx.BatchResults) error {
		if _, err := r.Exec(); err != nil {
			return fmt.Errorf("cairn: advancing %s: %w", stream, err)
		}
		return nil
	}, `
INSERT INTO cairn.streams (stream, kind, seq) VALUES ($1, $2, $3)
ON CONFLICT (stream) DO UPDATE SET seq = EXCLUDED.seq, updated_at = now()`,
		e.Stream, e.Kind, e.Seq)

	b.own(func(r pgx.BatchResults) error {
		var txid string
		if err := r.QueryRow().Scan(&e.Position, &txid); err != nil {
			return fmt.Errorf("cairn: appending to %s: %w", stream, err)
		}
		var err error
		e.TxID, err = strconv.ParseUint(txid, 10, 64)
		return err
	}, `
INSERT INTO cairn.entries (stream, kind, seq, message, result, refusal, events, effects, idempotency_key,
    actor, via, correlation_id, causation_id, occurred_at, extra, recorded_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9,
    nullif($10, ''), nullif($11, ''), nullif($12, ''), nullif($13, ''), $14, $15, $16)
RETURNING position, txid::text`,
		e.Stream, e.Kind, e.Seq, message, nullJSON(e.Result), refusal, events, effects, key,
		e.Meta.Actor, e.Meta.Via, e.Meta.CorrelationID, e.Meta.CausationID, occurred, extra, e.RecordedAt)

	// Every effect in one statement, numbered from 0 in order.
	if len(outbox) > 0 {
		b.own(func(r pgx.BatchResults) error {
			if _, err := r.Exec(); err != nil {
				return fmt.Errorf("cairn: queueing the effects of %s#%d: %w", stream, e.Seq, err)
			}
			return nil
		}, `
INSERT INTO cairn.outbox (stream, seq, idx, kind, effect)
SELECT $1, $2, i - 1, $3, effect::jsonb FROM unnest($4::text[]) WITH ORDINALITY AS t(effect, i)`,
			e.Stream, e.Seq, e.Kind, outbox)
	}
	l.seqs[e.Stream] = e.Seq
	return nil
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

// txid is read as text under another name: left named txid, it would shadow
// the column in ORDER BY txid, which would then sort the log as text ("1000"
// before "999").
const entryColumns = `position, txid::text AS txid_text, stream, kind, seq, message, result, refusal, events, effects,
coalesce(idempotency_key, ''), recorded_at,
coalesce(actor, ''), coalesce(via, ''), coalesce(correlation_id, ''), coalesce(causation_id, ''), occurred_at, extra`

func scanEntries(rows pgx.Rows) ([]Entry, error) {
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var (
			e                       Entry
			txid                    string
			message, events, effect []byte
			result, refusal         []byte
			occurred                *time.Time
			extra                   []byte
		)
		if err := rows.Scan(&e.Position, &txid, &e.Stream, &e.Kind, &e.Seq, &message, &result, &refusal,
			&events, &effect, &e.IdempotencyKey, &e.RecordedAt,
			&e.Meta.Actor, &e.Meta.Via, &e.Meta.CorrelationID, &e.Meta.CausationID, &occurred, &extra); err != nil {
			return nil, err
		}
		if occurred != nil {
			e.Meta.OccurredAt = *occurred
		}
		if string(extra) != "{}" {
			if err := json.Unmarshal(extra, &e.Meta.Extra); err != nil {
				return nil, fmt.Errorf("cairn: %s#%d metadata: %w", e.Stream, e.Seq, err)
			}
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
