// Package ledger is the Postgres shell around a cairn.Decider: it loads a
// stream's state, decides a message, and records the message, its decision,
// its events and its effects in one transaction.
//
// Every decided message the ledger can store is an entry, refused ones
// included, so the ledger is a complete record of what was asked and what was
// answered. A message carrying text the database cannot hold (a NUL, or text
// that is not valid UTF-8) is refused by the decider and answered without an
// entry, since the entry could not hold it. That record is
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
	"unicode/utf8"

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
	cache        *stateCache
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// RequireActor says whether every message must have an actor in its metadata.
// It is on by default, so every entry is attributed; tests turn it off.
func RequireActor(required bool) StoreOption { return func(s *Store) { s.requireActor = required } }

// DefaultStateCacheSize is how many streams' state a store keeps in memory
// unless told otherwise.
const DefaultStateCacheSize = 10_000

// StateCacheSize sets how many streams' state the store keeps in memory
// between messages, for every aggregate on it. Zero turns the cache off, and
// Handle then reads every stream before deciding. See docs/PERFORMANCE.md.
func StateCacheSize(streams int) StoreOption {
	return func(s *Store) {
		s.cache = nil
		if streams > 0 {
			s.cache = newStateCache(streams)
		}
	}
}

// New returns a store over pool. Call Migrate before first use.
//
// The store keeps the state of up to DefaultStateCacheSize streams in memory,
// so that Handle can record a message to a stream it has recently recorded to
// in one round trip; StateCacheSize changes that.
func New(pool *pgxpool.Pool, opts ...StoreOption) *Store {
	s := &Store{pool: pool, requireActor: true, cache: newStateCache(DefaultStateCacheSize)}
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

// parseStream is the Stream a stored name and kind denote: the name joins the
// two with a slash, so the ID is what follows the kind.
func parseStream(name, kind string) Stream {
	return Stream{Kind: kind, ID: strings.TrimPrefix(name, kind+"/")}
}

// Entry is one decided message as stored.
type Entry struct {
	Position int64
	TxID     uint64
	Stream   Stream
	Seq      int64
	Message  codec.Envelope
	// Result is nil when the message was refused.
	Result         *codec.Envelope
	Refusal        *cairn.Refusal
	Events         []codec.Envelope
	Effects        []codec.Envelope
	IdempotencyKey string
	Meta           Meta
	// RecordedAt is when the recording process recorded the entry, by its own
	// clock: one transaction's entries share it. The log is ordered by TxID
	// and Position, never by time, so clocks that differ between processes
	// can't reorder it.
	RecordedAt time.Time
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
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Tx is a transaction that may write to the ledger: the streams Transact
// locked, and nothing else. It runs a caller's own statements too, each in a
// round trip of its own; Transact owns its begin, commit and rollback.
type Tx struct {
	tx    pgx.Tx
	locks *locks
}

// Exec runs a statement.
func (tx *Tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return tx.tx.Exec(ctx, sql, args...)
}

// Query runs a query.
func (tx *Tx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return tx.tx.Query(ctx, sql, args...)
}

// QueryRow runs a query that returns one row.
func (tx *Tx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return tx.tx.QueryRow(ctx, sql, args...)
}

// SendBatch runs several statements in one round trip.
func (tx *Tx) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return tx.tx.SendBatch(ctx, b)
}

// locks is what a transaction's Lock found: the seq of each stream it locked,
// advanced as entries are queued, and the recorded_at every entry the
// transaction records shares.
type locks struct {
	seqs map[string]int64
	at   time.Time
}

// recordingTime is the recorded_at of entries recorded now. It comes from
// this process, not the database, so that it is known before the entry is
// sent, and is cut to the microseconds Postgres keeps.
func recordingTime() time.Time { return time.Now().Truncate(time.Microsecond) }

// ErrNotLocked is returned when a transaction writes a stream it did not lock.
var ErrNotLocked = errors.New("cairn: stream is not locked in this transaction")

// Transact runs fn in a transaction that holds the write lock on streams,
// committing if fn returns nil. Pass every stream the transaction will write;
// Apply on any other fails with ErrNotLocked. Locking a stream that does not
// exist yet does not create it: its row is written with its first entry, so a
// stream locked only to be read leaves nothing behind.
//
// The locks are taken before anything else, in a fixed order, so two
// transactions that lock overlapping streams can never deadlock. The global
// log is ordered by transaction ID, and Postgres assigns that ID at a
// transaction's first write. Every stream lock is therefore an advisory lock
// taken while the transaction has no ID yet: a transaction that queues behind
// another for a stream is assigned its ID after the other's, so its entries
// follow the other's in the log as they do in the stream.
//
// fn may run its own statements on tx, such as claiming a number from an
// application table. Each costs a round trip; see docs/CONVENTIONS.md.
func (s *Store) Transact(ctx context.Context, streams []Stream, fn func(tx *Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(ptx pgx.Tx) error {
		tx := &Tx{tx: ptx}
		if len(streams) > 0 {
			var b Batch
			l, err := queueLock(&b, streams)
			if err != nil {
				return err
			}
			if err := b.send(ctx, ptx); err != nil {
				return err
			}
			tx.locks = l
		}
		return fn(tx)
	})
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
		if err := validStream(st); err != nil {
			return nil, err
		}
		names[i] = st.String()
	}

	l := &locks{seqs: make(map[string]int64, len(names)), at: recordingTime()}
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
func queueLockAt(b *Batch, stream Stream, seq int64) (*locks, error) {
	if err := validStream(stream); err != nil {
		return nil, err
	}
	b.own(func(r pgx.BatchResults) error {
		_, err := r.Exec()
		return lockError(err)
	}, `SELECT cairn.lock_stream_at($1, $2)`, stream.String(), seq)
	return &locks{seqs: map[string]int64{stream.String(): seq}, at: recordingTime()}, nil
}

// maxIndexedTextBytes bounds every text value cairn puts in a btree index.
// Postgres cannot build a btree entry larger than about 2704 bytes, and a
// composite index spends that budget on all of its columns, so a stream name
// and an idempotency key are each bounded well below it. The limit is bytes,
// not runes, because that is how Postgres measures UTF-8 text.
const maxIndexedTextBytes = 1024

// maxQuotedTextBytes bounds how much of a rejected value an error quotes. The
// caller already has the value it sent, so echoing an 8KB one back costs it
// 8KB of log or response and tells it nothing it did not know.
const maxQuotedTextBytes = 64

// quoteText renders v for an error message: all of it when it is short enough
// to be worth reading, and otherwise at most maxQuotedTextBytes, cut on a rune
// boundary, with an ellipsis showing the rest was left out.
func quoteText(v string) string {
	if len(v) <= maxQuotedTextBytes {
		return fmt.Sprintf("%q", v)
	}
	cut := maxQuotedTextBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	return fmt.Sprintf("%q…", v[:cut])
}

// validStream refuses a stream whose name could be another's (the name joins
// kind and ID with a slash, so a kind may not contain one), that Postgres
// cannot store, or whose name is too long for the btree indexes that key on
// it. A NUL, invalid UTF-8 or an over-long name would otherwise reach the
// driver as a text parameter and fail with a raw SQLSTATE.
func validStream(st Stream) error {
	if strings.Contains(st.Kind, "/") || st.Kind == "" || st.ID == "" {
		return fmt.Errorf("cairn: invalid stream %q", st)
	}
	if !cairn.StorableText(st.Kind) || !cairn.StorableText(st.ID) {
		return fmt.Errorf("cairn: invalid stream %q: the name is not valid UTF-8 or contains a NUL", st)
	}
	if n := len(st.String()); n > maxIndexedTextBytes {
		return fmt.Errorf("cairn: invalid stream %s: the name is %d bytes, longer than the %d-byte limit",
			quoteText(st.String()), n, maxIndexedTextBytes)
	}
	return nil
}

// validKind refuses a kind that could be another's, that Postgres cannot
// store, or that is too long for the btree indexes that key on it, for the
// paths that filter by kind alone.
func validKind(kind string) error {
	if kind == "" || strings.Contains(kind, "/") {
		return fmt.Errorf("cairn: invalid stream kind %q", kind)
	}
	if !cairn.StorableText(kind) {
		return fmt.Errorf("cairn: invalid stream kind %q: the name is not valid UTF-8 or contains a NUL", kind)
	}
	if n := len(kind); n > maxIndexedTextBytes {
		return fmt.Errorf("cairn: invalid stream kind %s: the kind is %d bytes, longer than the %d-byte limit",
			quoteText(kind), n, maxIndexedTextBytes)
	}
	return nil
}

// minTimestamptz and maxTimestamptz bound what Postgres's timestamptz can
// hold: 4713 BC to 294276 AD. A producer-supplied OccurredAt outside them
// reaches the server as a parameter and comes back as a raw SQLSTATE, so
// queueAppend refuses it before it is sent.
var (
	minTimestamptz = time.Date(-4712, time.January, 1, 0, 0, 0, 0, time.UTC)
	maxTimestamptz = time.Date(294276, time.December, 31, 23, 59, 59, 999999000, time.UTC)
)

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
	e.Stream, e.Seq, e.RecordedAt = stream, seq+1, at

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
	var result []byte
	if e.Result != nil {
		if result, err = json.Marshal(e.Result); err != nil {
			return err
		}
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
	if n := len(e.IdempotencyKey); e.IdempotencyKey != "" && n > maxIndexedTextBytes {
		return fmt.Errorf("cairn: the idempotency key %s for %s is %d bytes, longer than the %d-byte limit",
			quoteText(e.IdempotencyKey), stream, n, maxIndexedTextBytes)
	}
	for _, text := range []struct {
		name  string
		value string
	}{
		{"actor", e.Meta.Actor},
		{"via", e.Meta.Via},
		{"correlation ID", e.Meta.CorrelationID},
		{"causation ID", e.Meta.CausationID},
	} {
		if !cairn.StorableText(text.value) {
			return fmt.Errorf("cairn: metadata %s for %s is not valid UTF-8 or contains a NUL", text.name, stream)
		}
		if n := len(text.value); n > maxIndexedTextBytes {
			return fmt.Errorf("cairn: metadata %s %s for %s is %d bytes, longer than the %d-byte limit",
				text.name, quoteText(text.value), stream, n, maxIndexedTextBytes)
		}
	}
	extra := []byte(`{}`)
	if len(e.Meta.Extra) > 0 {
		if err := codec.CheckStorable(e.Meta.Extra); err != nil {
			return fmt.Errorf("cairn: encoding metadata for %s: %w", stream, err)
		}
		if extra, err = json.Marshal(e.Meta.Extra); err != nil {
			return fmt.Errorf("cairn: encoding metadata for %s: %w", stream, err)
		}
	}
	var occurred *time.Time
	if !e.Meta.OccurredAt.IsZero() {
		if e.Meta.OccurredAt.Before(minTimestamptz) || e.Meta.OccurredAt.After(maxTimestamptz) {
			return fmt.Errorf("cairn: occurred_at for %s is outside the range Postgres can store", stream)
		}
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
		stream.String(), stream.Kind, e.Seq)

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
		stream.String(), stream.Kind, e.Seq, jsonArg(message), jsonArg(result), jsonArg(refusal), jsonArg(events), jsonArg(effects), key,
		e.Meta.Actor, e.Meta.Via, e.Meta.CorrelationID, e.Meta.CausationID, occurred, jsonArg(extra), e.RecordedAt)

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
			stream.String(), e.Seq, stream.Kind, outbox)
	}
	l.seqs[stream.String()] = e.Seq
	return nil
}

// jsonArg passes encoded JSON to a json or jsonb parameter as text, or NULL
// when there is none. Sent as []byte, it would reach the server as bytea under
// pgx's simple protocol, which carries no parameter types, and fail to parse.
func jsonArg(data []byte) any {
	if data == nil {
		return nil
	}
	return string(data)
}

func nonNil(envs []codec.Envelope) []codec.Envelope {
	if envs == nil {
		return []codec.Envelope{}
	}
	return envs
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
			txid, name, kind        string
			message, events, effect []byte
			result, refusal         []byte
			occurred                *time.Time
			extra                   []byte
		)
		if err := rows.Scan(&e.Position, &txid, &name, &kind, &e.Seq, &message, &result, &refusal,
			&events, &effect, &e.IdempotencyKey, &e.RecordedAt,
			&e.Meta.Actor, &e.Meta.Via, &e.Meta.CorrelationID, &e.Meta.CausationID, &occurred, &extra); err != nil {
			return nil, err
		}
		e.Stream = parseStream(name, kind)
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
		if result != nil {
			e.Result = &codec.Envelope{}
			if err := json.Unmarshal(result, e.Result); err != nil {
				return nil, fmt.Errorf("cairn: %s#%d result: %w", e.Stream, e.Seq, err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func readStream(ctx context.Context, q querier, stream Stream, afterSeq int64) ([]Entry, error) {
	if err := validStream(stream); err != nil {
		return nil, err
	}
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

// logFence measures the global log's read cutoff and stores it in the
// transaction-local setting cairn.fence, for the read that follows it in the
// same batch.
//
// Transaction IDs are assigned by the cluster, so a cutoff measured from the
// snapshot views alone (pg_snapshot_xmin on the whole server) would let an
// idle transaction in another database hold this one's committed entries
// back. Only a transaction in this database can write to cairn.entries here,
// so the fence is this database's smallest in-flight transaction ID, not the
// cluster's. pg_stat_activity supplies the ordinary transactions and
// pg_prepared_xacts the prepared ones — a prepared transaction has no
// backend — and widenedXid widens both sources' 32-bit xids, which carry no
// epoch, onto the xid8 scale cairn.entries.txid is stored and ordered on.
//
// The reference the widening reads against is measured alongside: this
// statement's own xid (pg_current_xact_id assigns one when the transaction
// has none), plus one — the next xid the cluster will hand out, split into
// its low 32 bits and its epoch. Every live transaction's ID was handed out
// within one wrap of the next one to be assigned (Postgres's wraparound
// handling refuses new xids long before a live transaction could fall two
// wraps behind), so the widening is exact under that reference: an xid above
// the reference's low half is the wrap it came from, so it belongs to the
// previous epoch; anything else is the current one.
//
// pg_snapshot_xmax was the other candidate and is wrong: it is the latest
// completed transaction plus one, not the next to assign, and a transaction
// whose xid is ahead of that completion frontier (the newest assigned, still
// in flight under any load) widens a wrap too low. The fence would sit
// below every stored entry for as long as one of them is open — under write
// load, always — which is what the epoch-less cast did to begin with.
//
// Measuring in a statement of its own, before the read's snapshot, is what
// makes the cutoff sound. A transaction that is in flight when the read
// takes its snapshot either already existed at the measurement, and is in
// the measured set with its widened true value, or was assigned its
// transaction ID after — that is, at or above this statement's own xid — and
// the fence never rises above that. Measuring inside the read would be
// unsound: its snapshot is taken before its own subqueries read
// pg_stat_activity, so a transaction that commits in between is neither
// visible to the snapshot nor counted in the in-flight set.
//
// The read's snapshot has to be taken after the fence statement's. Under
// READ COMMITTED every statement gets its own, so the batch below holds.
// Under REPEATABLE READ a transaction reuses the snapshot its first statement
// took, and the first statement of CatchUp's transaction — its checkpoint
// read — predates the fence, so a transaction that commits after the fence
// but before the read would be invisible to the read while its cursor
// position has already been passed. CatchUp's transactions are therefore
// begun READ COMMITTED; a caller of the internal readAll that opens its own
// transaction must do the same.
//
// The fence must also be the first read of pg_stat_activity in its
// transaction: that view is cached for the transaction's lifetime, so a
// second readAll in the same transaction would measure the in-flight set as
// it stood at the transaction's first read and could let the cutoff pass a
// transaction that started since, skipping its entries. CatchUp opens one
// transaction per batch to keep this true.
//
// backend_xid is visible to every role on the servers this runs against:
// PostgreSQL 17 made it unconditional, so a deployment with several writer
// roles does not lose the guard and nothing here needs a fallback for a
// privilege gap. (Verified against 17.11, as an unprivileged role watching a
// transaction held open in another session.) On older servers the column is
// hidden without pg_read_all_stats; such a deployment needs that privilege.
//
// One blind spot the widening cannot treat differently from the exact case:
// a 32-bit xid carries no epoch, so nothing can say how many wraps an older
// one is past. A live transaction is within one wrap by construction, but a
// prepared one can sit across any number of them. A prepared transaction
// still open after the cluster has consumed another 2^32 xids widens a wrap
// too high, and a reader could pass its commit. pg_prepared_xacts is meant
// to be emptied out — a prepared transaction is committed or rolled back in
// ordinary operation, and cairn's own tests roll theirs back — so the fence
// assumes nothing is still there across a wrap.
const widenedXid = `CASE WHEN x.thumb > ref.low
       THEN (ref.epoch - 1) * 4294967296::numeric + x.thumb
       ELSE            ref.epoch * 4294967296::numeric + x.thumb
  END::text::xid8`

const fenceRef = `
    (SELECT (own.xid + 1) % 4294967296::numeric AS low,
            floor((own.xid + 1) / 4294967296::numeric) AS epoch
       FROM (SELECT pg_current_xact_id()::text::numeric AS xid) AS own)`

const logFence = `SELECT set_config('cairn.fence', COALESCE((
  SELECT min(` + widenedXid + `)
  FROM (SELECT backend_xid::text::numeric AS thumb FROM pg_stat_activity
        WHERE datid = (SELECT oid FROM pg_database WHERE datname = current_database())
        UNION ALL
        SELECT transaction::text::numeric FROM pg_prepared_xacts
        WHERE database = current_database()) AS x(thumb)
  CROSS JOIN ` + fenceRef + ` AS ref
), '18446744073709551615'::xid8)::text, true)`

func readAll(ctx context.Context, q querier, after Cursor, limit int, kinds []string) ([]Entry, error) {
	for _, kind := range kinds {
		if err := validKind(kind); err != nil {
			return nil, err
		}
	}
	sql := `SELECT ` + entryColumns + ` FROM cairn.entries
WHERE (txid, position) > ($1::text::xid8, $2)
  AND txid < current_setting('cairn.fence')::xid8`
	args := []any{strconv.FormatUint(after.TxID, 10), after.Position}
	if len(kinds) > 0 {
		sql += ` AND kind = ANY($3)`
		args = append(args, kinds)
	}
	if limit <= 0 {
		limit = 100
	}
	sql += fmt.Sprintf(` ORDER BY txid, position LIMIT %d`, limit)

	// The fence and the read go out together, in one round trip, so CatchUp
	// stays within its budget. pgx runs a batch in an implicit transaction, so
	// the read sees the setting the fence statement made.
	var b pgx.Batch
	b.Queue(logFence)
	b.Queue(sql, args...)
	results := q.SendBatch(ctx, &b)
	var fence string
	if err := results.QueryRow().Scan(&fence); err != nil {
		_ = results.Close()
		return nil, fmt.Errorf("cairn: measuring the log fence: %w", err)
	}
	rows, err := results.Query()
	if err != nil {
		_ = results.Close()
		return nil, fmt.Errorf("cairn: reading the log: %w", err)
	}
	entries, err := scanEntries(rows)
	if closeErr := results.Close(); err == nil {
		err = closeErr
	}
	return entries, err
}

// ReadAll returns up to limit entries after the cursor, in transaction order
// (each stream's entries in seq order), optionally only of the given kinds.
// Entries from a transaction still in flight in this database are held back
// until every older one here has finished, so a reader that advances its
// cursor past what it read never misses an entry. A transaction open in
// another database on the same server does not hold the log back.
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
	if err := validKind(kind); err != nil {
		return nil, err
	}
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
		out = append(out, parseStream(n, kind))
	}
	return out, nil
}
