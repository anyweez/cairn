package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
)

// Aggregate binds a Decider to the ledger: one kind of stream, its codecs,
// and how its state is snapshotted.
type Aggregate[S, M, R, Ev, Ef any] struct {
	Store   *Store
	Kind    string
	Decider cairn.Decider[S, M, R, Ev, Ef]

	Messages *codec.Codec[M]
	Events   *codec.Codec[Ev]
	Effects  *codec.Codec[Ef]

	// StateVersion names the shape of S as stored in snapshots. Change it
	// whenever S changes shape; snapshots of any other version are ignored and
	// state is rebuilt from events. Each version keeps its own snapshot, so
	// processes running different versions side by side don't evict each
	// other's; those of retired versions can be deleted. Empty disables
	// snapshots.
	StateVersion string
	// SnapshotEvery is how many entries may accumulate past the last snapshot
	// before another is taken. Defaults to 100.
	SnapshotEvery int64

	// Views are written in the same transaction as every message that
	// produces events.
	Views []View[S]

	// Actor, when set, returns the actor a message itself names (its "by"
	// field), which then fills Meta.Actor. A message whose metadata names a
	// different actor is not recorded, so the two can never disagree.
	Actor func(M) string
}

// Stream names one of this aggregate's streams.
func (a *Aggregate[S, M, R, Ev, Ef]) Stream(id string) Stream { return Stream{Kind: a.Kind, ID: id} }

// Loaded is a stream's state as of a sequence number.
type Loaded[S any] struct {
	Stream Stream
	State  S
	Seq    int64

	snapshotSeq int64
}

// Handled is what recording one message produced.
type Handled[S, R any] struct {
	Entry   Entry
	State   S
	Result  R
	Refusal *cairn.Refusal
	// Duplicate is true when the idempotency key matched an earlier entry:
	// nothing was decided or recorded, and Result and Refusal are the earlier
	// entry's. State is the stream's current state.
	Duplicate bool
}

// Option adjusts how a message is recorded.
type Option func(*options)

type options struct {
	key  string
	meta Meta
}

// IdempotencyKey makes a message safe to retry: a second message with the same
// key on the same stream is not decided again, and the first one's answer is
// returned.
func IdempotencyKey(key string) Option { return func(o *options) { o.key = key } }

func collect(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Handle decides msg against the stream id and records the result, in its own
// transaction. A refusal is recorded and committed, then returned as the error
// (a *cairn.Refusal) alongside the populated Handled.
//
// It costs two round trips. The first begins the transaction, locks the
// stream and reads it; the second records the decision and commits, or for a
// duplicate, rolls back.
func (a *Aggregate[S, M, R, Ev, Ef]) Handle(ctx context.Context, id string, msg M, opts ...Option) (Handled[S, R], error) {
	h, err := a.handle(ctx, id, msg, collect(opts))
	if err != nil {
		return Handled[S, R]{}, err
	}
	if h.Refusal != nil {
		return h, h.Refusal
	}
	return h, nil
}

func (a *Aggregate[S, M, R, Ev, Ef]) handle(ctx context.Context, id string, msg M, o options) (Handled[S, R], error) {
	stream := a.Stream(id)
	conn, err := a.Store.pool.Acquire(ctx)
	if err != nil {
		return Handled[S, R]{}, err
	}
	defer release(conn)

	var read Batch
	read.own(sqlStep("beginning a transaction"), `BEGIN`)
	l, err := queueLock(&read, []Stream{stream})
	if err != nil {
		return Handled[S, R]{}, err
	}
	load := a.queueLoad(&read, stream)
	var dup *pendingDuplicate
	if o.key != "" {
		dup = queueDuplicate(&read, stream, o.key)
	}
	if err := read.send(ctx, conn); err != nil {
		return Handled[S, R]{}, err
	}
	at, err := a.finishLoad(ctx, conn, load, l.seqs[stream.String()])
	if err != nil {
		return Handled[S, R]{}, err
	}
	// A retry gets its first answer even when it lost the request's metadata:
	// the message was attributed when it was recorded.
	if dup != nil && dup.found {
		return a.duplicateOf(dup.entry, at.State)
	}
	meta, err := a.meta(ctx, msg, o)
	if err != nil {
		return Handled[S, R]{}, err
	}

	var write Batch
	finish, next, err := a.queueApply(&write, l, at, msg, o, meta)
	if err != nil {
		return Handled[S, R]{}, err
	}
	write.own(commitStep, `COMMIT`)
	if err := write.send(ctx, conn); err != nil {
		return Handled[S, R]{}, viewError(err, next)
	}
	return finish(), nil
}

// release returns conn to the pool, rolling back any transaction a failure
// left open: the pool closes a connection released mid-transaction, and
// opening another costs several round trips.
func release(conn *pgxpool.Conn) {
	pg := conn.Conn().PgConn()
	if !pg.IsClosed() && pg.TxStatus() != 'I' {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = conn.Exec(ctx, `ROLLBACK`)
		cancel()
	}
	conn.Release()
}

// sqlStep reads a statement that returns nothing worth keeping.
func sqlStep(doing string) func(pgx.BatchResults) error {
	return func(r pgx.BatchResults) error {
		if _, err := r.Exec(); err != nil {
			return fmt.Errorf("cairn: %s: %w", doing, err)
		}
		return nil
	}
}

func commitStep(r pgx.BatchResults) error {
	tag, err := r.Exec()
	if err != nil {
		return fmt.Errorf("cairn: committing: %w", err)
	}
	if tag.String() != "COMMIT" {
		return fmt.Errorf("cairn: committing: the transaction was rolled back (%s)", tag)
	}
	return nil
}

// Load reads a stream's current state without locking it, in one round trip.
func (a *Aggregate[S, M, R, Ev, Ef]) Load(ctx context.Context, id string) (Loaded[S], error) {
	stream := a.Stream(id)
	var (
		b   Batch
		seq int64
	)
	b.own(sqlStep("beginning a transaction"), `BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY`)
	b.own(func(r pgx.BatchResults) error {
		// A stream that does not exist yet reads as seq 0, with no entries.
		if err := r.QueryRow().Scan(&seq); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("cairn: reading %s: %w", stream, err)
		}
		return nil
	}, `SELECT seq FROM cairn.streams WHERE stream = $1`, stream.String())
	load := a.queueLoad(&b, stream)
	b.own(commitStep, `COMMIT`)

	conn, err := a.Store.pool.Acquire(ctx)
	if err != nil {
		return Loaded[S]{}, err
	}
	defer release(conn)
	if err := b.send(ctx, conn); err != nil {
		return Loaded[S]{}, err
	}
	// Any read finishLoad needs goes on this connection: asking the pool for
	// another while holding this one can wait forever on a full pool.
	return a.finishLoad(ctx, conn, load, seq)
}

// LoadLocked reads the state of a stream the transaction has locked.
func (a *Aggregate[S, M, R, Ev, Ef]) LoadLocked(ctx context.Context, tx *Tx, id string) (Loaded[S], error) {
	stream := a.Stream(id)
	seq, err := tx.locks.seq(stream)
	if err != nil {
		return Loaded[S]{}, err
	}
	var b Batch
	load := a.queueLoad(&b, stream)
	if err := b.send(ctx, tx.Tx); err != nil {
		return Loaded[S]{}, err
	}
	return a.finishLoad(ctx, tx, load, seq)
}

// pendingLoad is a stream's snapshot and the events after it, as read by a
// batch that has yet to run.
type pendingLoad struct {
	stream   Stream
	snapshot []byte
	snapSeq  int64
	entries  []Entry // Seq and Events only
}

// queueLoad queues reading a stream's snapshot and every entry after it. Only
// the entries' events are read: they are all a fold needs.
func (a *Aggregate[S, M, R, Ev, Ef]) queueLoad(b *Batch, stream Stream) *pendingLoad {
	p := &pendingLoad{stream: stream}
	name := stream.String()
	if a.StateVersion != "" {
		b.own(func(r pgx.BatchResults) error {
			var data []byte
			err := r.QueryRow().Scan(&p.snapSeq, &data)
			switch {
			case err == nil:
				p.snapshot = append([]byte(nil), data...)
			case !errors.Is(err, pgx.ErrNoRows):
				return fmt.Errorf("cairn: reading snapshot of %s: %w", stream, err)
			}
			return nil
		}, `SELECT seq, state FROM cairn.state_snapshots WHERE stream = $1 AND version = $2`, name, a.StateVersion)
	}
	b.own(func(r pgx.BatchResults) error {
		rows, err := r.Query()
		if err == nil {
			var (
				seq    int64
				events []byte
			)
			_, err = pgx.ForEachRow(rows, []any{&seq, &events}, func() error {
				e := Entry{Seq: seq}
				if err := json.Unmarshal(events, &e.Events); err != nil {
					return fmt.Errorf("%s#%d events: %w", stream, seq, err)
				}
				p.entries = append(p.entries, e)
				return nil
			})
		}
		if err != nil {
			return fmt.Errorf("cairn: reading %s: %w", stream, err)
		}
		return nil
	}, `
SELECT seq, events FROM cairn.entries
WHERE stream = $1
  AND seq > coalesce((SELECT seq FROM cairn.state_snapshots WHERE stream = $1 AND version = $2), 0)
ORDER BY seq`, name, a.StateVersion)
	return p
}

// finishLoad folds what p read into the stream's state as of seq.
func (a *Aggregate[S, M, R, Ev, Ef]) finishLoad(ctx context.Context, q querier, p *pendingLoad, seq int64) (Loaded[S], error) {
	at := Loaded[S]{Stream: p.stream, State: a.Decider.Initial(), Seq: seq}
	entries := p.entries
	switch {
	case p.snapshot != nil && p.snapSeq <= seq:
		var state S
		if err := json.Unmarshal(p.snapshot, &state); err != nil {
			return Loaded[S]{}, fmt.Errorf("cairn: decoding snapshot of %s: %w", p.stream, err)
		}
		at.State, at.snapshotSeq = state, p.snapSeq
	case p.snapshot != nil:
		// A snapshot past the stream's seq can only come from damaged data.
		// Ignore it, and fold every entry instead.
		all, err := readStream(ctx, q, p.stream, 0)
		if err != nil {
			return Loaded[S]{}, err
		}
		entries = all
	}
	for _, e := range entries {
		if e.Seq > seq {
			break
		}
		events, err := a.Events.DecodeAll(e.Events)
		if err != nil {
			return Loaded[S]{}, fmt.Errorf("cairn: %s#%d: %w", p.stream, e.Seq, err)
		}
		at.State = a.Decider.Fold(at.State, events)
	}
	return at, nil
}

// Apply decides msg against a locked, loaded stream and records the result in
// tx, updating at to the state after it. A refusal is not an error here: it is
// recorded like any other decision and reported in Handled.Refusal, so a
// caller composing several streams decides for itself whether to commit.
//
// It costs one round trip, and one more to look up an idempotency key.
func (a *Aggregate[S, M, R, Ev, Ef]) Apply(ctx context.Context, tx *Tx, at *Loaded[S], msg M, opts ...Option) (Handled[S, R], error) {
	o := collect(opts)
	// A retry gets its first answer even when it lost the request's metadata:
	// the message was attributed when it was recorded.
	if o.key != "" {
		var b Batch
		dup := queueDuplicate(&b, at.Stream, o.key)
		if err := b.send(ctx, tx.Tx); err != nil {
			return Handled[S, R]{}, err
		}
		if dup.found {
			return a.duplicateOf(dup.entry, at.State)
		}
	}
	meta, err := a.meta(ctx, msg, o)
	if err != nil {
		return Handled[S, R]{}, err
	}

	var b Batch
	finish, next, err := a.queueApply(&b, tx.locks, *at, msg, o, meta)
	if err != nil {
		return Handled[S, R]{}, err
	}
	if err := b.send(ctx, tx.Tx); err != nil {
		return Handled[S, R]{}, viewError(err, next)
	}
	*at = next
	return finish(), nil
}

// queueApply decides msg against at and queues recording the decision: its
// entry and effects, a snapshot when one is due, and the views. next is the
// stream's state after it; finish returns what was recorded, once the batch
// has run.
func (a *Aggregate[S, M, R, Ev, Ef]) queueApply(b *Batch, l *locks, at Loaded[S], msg M, o options, meta Meta) (finish func() Handled[S, R], next Loaded[S], err error) {
	decision := a.Decider.Decide(at.State, msg)
	if decision.Refused() && (len(decision.Events) > 0 || len(decision.Effects) > 0) {
		return nil, at, fmt.Errorf("cairn: %s refused %s but returned events or effects", at.Stream, decision.Refusal.Code)
	}

	entry := &Entry{Refusal: decision.Refusal, IdempotencyKey: o.key, Meta: meta}
	if entry.Message, err = a.Messages.Encode(msg); err != nil {
		return nil, at, err
	}
	if entry.Events, err = a.Events.EncodeAll(decision.Events); err != nil {
		return nil, at, err
	}
	if entry.Effects, err = a.Effects.EncodeAll(decision.Effects); err != nil {
		return nil, at, err
	}
	if !decision.Refused() {
		if entry.Result, err = json.Marshal(decision.Result); err != nil {
			return nil, at, err
		}
	}

	if err := l.queueAppend(b, at.Stream, entry); err != nil {
		return nil, at, err
	}
	next = at
	next.State = a.Decider.Fold(at.State, decision.Events)
	next.Seq = entry.Seq

	if err := a.queueSnapshot(b, &next); err != nil {
		return nil, at, err
	}
	if len(decision.Events) > 0 {
		if err := a.queueViews(b, next, *entry); err != nil {
			return nil, at, err
		}
	}

	return func() Handled[S, R] {
		return Handled[S, R]{Entry: *entry, State: next.State, Result: decision.Result, Refusal: decision.Refusal}
	}, next, nil
}

// pendingDuplicate is the entry an idempotency key names, if any, as read by
// a batch that has yet to run.
type pendingDuplicate struct {
	found bool
	entry Entry
}

func queueDuplicate(b *Batch, stream Stream, key string) *pendingDuplicate {
	p := &pendingDuplicate{}
	b.own(func(r pgx.BatchResults) error {
		rows, err := r.Query()
		if err != nil {
			return fmt.Errorf("cairn: looking up %s's idempotency key: %w", stream, err)
		}
		entries, err := scanEntries(rows)
		if err != nil {
			return fmt.Errorf("cairn: looking up %s's idempotency key: %w", stream, err)
		}
		if len(entries) > 0 {
			p.found, p.entry = true, entries[0]
		}
		return nil
	}, `SELECT `+entryColumns+` FROM cairn.entries WHERE stream = $1 AND idempotency_key = $2`, stream.String(), key)
	return p
}

// duplicateOf answers a retry with the entry that first answered it.
func (a *Aggregate[S, M, R, Ev, Ef]) duplicateOf(e Entry, state S) (Handled[S, R], error) {
	h := Handled[S, R]{Entry: e, State: state, Refusal: e.Refusal, Duplicate: true}
	if len(e.Result) > 0 {
		if err := json.Unmarshal(e.Result, &h.Result); err != nil {
			return Handled[S, R]{}, fmt.Errorf("cairn: decoding result of %s#%d: %w", e.Stream, e.Seq, err)
		}
	}
	return h, nil
}

// queueSnapshot queues a snapshot of at when one is due.
func (a *Aggregate[S, M, R, Ev, Ef]) queueSnapshot(b *Batch, at *Loaded[S]) error {
	if a.StateVersion == "" {
		return nil
	}
	every := a.SnapshotEvery
	if every <= 0 {
		every = 100
	}
	if at.Seq-at.snapshotSeq < every {
		return nil
	}
	data, err := json.Marshal(at.State)
	if err != nil {
		return fmt.Errorf("cairn: encoding snapshot of %s: %w", at.Stream, err)
	}
	b.own(sqlStep("saving snapshot of "+at.Stream.String()), `
INSERT INTO cairn.state_snapshots (stream, seq, version, state) VALUES ($1, $2, $3, $4)
ON CONFLICT (stream, version) DO UPDATE SET seq = excluded.seq, state = excluded.state, taken_at = now()`,
		at.Stream.String(), at.Seq, a.StateVersion, data)
	at.snapshotSeq = at.Seq
	return nil
}
