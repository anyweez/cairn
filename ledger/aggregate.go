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

	// Clone, when set, returns a copy of a state that shares nothing with it.
	// The store's state cache needs one (see StateCacheSize), so that a
	// caller can never change the state it keeps. Without Clone, a state with
	// no maps, slices, pointers or interfaces in it is copied by assignment;
	// otherwise one is copied through JSON when StateVersion is set, since
	// snapshots already rely on it surviving JSON; otherwise the aggregate's
	// streams are not cached.
	Clone func(S) S
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
// It costs one round trip when the store's state cache holds the stream: the
// message is decided at once, and recorded on condition that the stream is
// still where the cache has it. Otherwise it costs two. The first begins the
// transaction, locks the stream and reads it; the second records the decision
// and commits, or for a duplicate, rolls back. Messages that arrive together
// for one stream are recorded together. See docs/PERFORMANCE.md.
func (a *Aggregate[S, M, R, Ev, Ef]) Handle(ctx context.Context, id string, msg M, opts ...Option) (Handled[S, R], error) {
	o := collect(opts)
	r := &request[S, M, R]{ctx: ctx, msg: msg, o: o}
	r.meta, r.metaErr = a.meta(ctx, msg, o)

	var res result[S, R]
	if a.Store.cache != nil && a.copier() != nil {
		res = a.handleCached(ctx, a.Stream(id), r)
	} else if results, _, _, err := a.runGroup(ctx, a.Stream(id), []*request[S, M, R]{r}, nil); err != nil {
		res.err = err
	} else {
		res = results[0]
	}
	if res.err != nil {
		return Handled[S, R]{}, res.err
	}
	if res.h.Refusal != nil {
		return res.h, res.h.Refusal
	}
	return res.h, nil
}

// request is one message on its way to being recorded.
type request[S, M, R any] struct {
	ctx     context.Context
	msg     M
	o       options
	meta    Meta
	metaErr error
	done    chan result[S, R]
}

type result[S, R any] struct {
	h   Handled[S, R]
	err error
}

// errUnchecked answers a message that could not be decided without reading
// the stream first: one whose metadata is unusable may still be a retry,
// which is answered from its first entry.
var errUnchecked = errors.New("cairn: the message needs its idempotency key looked up")

// runGroup records reqs on one stream, in order, in one transaction, and
// returns each one's result and the stream's state before and after them.
//
// Given cached, the stream's state as this process last recorded it, it
// decides every message first and sends everything in one round trip, which
// fails with errStale unless the stream is still at cached.Seq. Idempotency
// keys are not looked up first: a duplicate fails the transaction on the
// key's unique index. Without cached, it reads the stream and looks up every
// key in a round trip of its own first.
//
// An error is the transaction's, and nothing in it was recorded when the
// database reported it (see retryable). Each request's own result says
// whether its message was refused, a duplicate, or failed before it was sent.
func (a *Aggregate[S, M, R, Ev, Ef]) runGroup(ctx context.Context, stream Stream, reqs []*request[S, M, R], cached *Loaded[S]) (results []result[S, R], before, after Loaded[S], err error) {
	conn, err := a.Store.pool.Acquire(ctx)
	if err != nil {
		return nil, Loaded[S]{}, Loaded[S]{}, err
	}
	defer release(conn)

	var (
		b    Batch
		l    *locks
		at   Loaded[S]
		dups = make([]*pendingDuplicate, len(reqs))
	)
	if cached != nil {
		// One batch, which Postgres runs as one implicit transaction: it
		// commits as the batch ends unless a statement fails, in which case
		// it rolls back with nothing left to end.
		l, at = queueLockAt(&b, stream, cached.Seq), *cached
	} else {
		b.own(sqlStep("beginning a transaction"), `BEGIN`)
		if l, err = queueLock(&b, []Stream{stream}); err != nil {
			return nil, Loaded[S]{}, Loaded[S]{}, err
		}
		load := a.queueLoad(&b, stream)
		for i, r := range reqs {
			if r.o.key != "" {
				dups[i] = queueDuplicate(&b, stream, r.o.key)
			}
		}
		if err := b.send(ctx, conn); err != nil {
			return nil, Loaded[S]{}, Loaded[S]{}, err
		}
		if at, err = a.finishLoad(ctx, conn, load, l.seqs[stream.String()]); err != nil {
			return nil, Loaded[S]{}, Loaded[S]{}, err
		}
		b = Batch{}
	}
	before = at

	results = make([]result[S, R], len(reqs))
	finishes := make([]func() Handled[S, R], len(reqs))
	var cause *Entry
	now := recordingTime()
	for i, r := range reqs {
		switch {
		// A retry gets its first answer even when it lost the request's
		// metadata: the message was attributed when it was recorded.
		case dups[i] != nil && dups[i].found:
			results[i].h, results[i].err = a.duplicateOf(dups[i].entry, at.State)
			continue
		case r.metaErr != nil && r.o.key != "" && cached != nil:
			results[i].err = errUnchecked
			continue
		case r.metaErr != nil:
			results[i].err = r.metaErr
			continue
		}
		// Each message's statements join the batch only once all of them are
		// queued, so one that fails leaves nothing of itself behind.
		var one Batch
		seq := l.seqs[stream.String()]
		finish, next, entry, err := a.queueEntry(&one, l, at, r.msg, r.o, r.meta, now)
		if err != nil {
			l.seqs[stream.String()] = seq
			results[i].err = err
			continue
		}
		b.merge(&one)
		if len(entry.Events) > 0 {
			cause = entry
		}
		at, finishes[i] = next, finish
	}
	if !anyRecorded(finishes) {
		// Nothing to record. A transaction already begun is rolled back as the
		// connection is released.
		return results, before, at, nil
	}

	// One snapshot and one write per view, of the state after every message:
	// views replace a stream's rows from its whole state.
	if err := a.queueSnapshot(&b, &at); err != nil {
		return nil, Loaded[S]{}, Loaded[S]{}, err
	}
	if cause != nil {
		if err := a.queueViews(&b, at, *cause); err != nil {
			return nil, Loaded[S]{}, Loaded[S]{}, err
		}
	}
	if cached == nil {
		b.own(commitStep, `COMMIT`)
	}
	if err := b.send(ctx, conn); err != nil {
		return nil, Loaded[S]{}, Loaded[S]{}, viewError(err, at)
	}
	for i, finish := range finishes {
		if finish != nil {
			results[i].h = finish()
		}
	}
	return results, before, at, nil
}

func anyRecorded[S, R any](finishes []func() Handled[S, R]) bool {
	for _, f := range finishes {
		if f != nil {
			return true
		}
	}
	return false
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
	seq, notLocked := tx.locks.seq(at.Stream)
	finish, next, entry, err := a.queueEntry(&b, tx.locks, *at, msg, o, meta, recordingTime())
	if err == nil {
		err = a.queueSnapshot(&b, &next)
	}
	if err == nil && len(entry.Events) > 0 {
		err = a.queueViews(&b, next, *entry)
	}
	if err != nil {
		// Nothing was sent, so the transaction can go on as if Apply hadn't
		// been called.
		if notLocked == nil {
			tx.locks.seqs[at.Stream.String()] = seq
		}
		return Handled[S, R]{}, err
	}
	if err := b.send(ctx, tx.Tx); err != nil {
		return Handled[S, R]{}, viewError(err, next)
	}
	*at = next
	return finish(), nil
}

// queueEntry decides msg against at and queues recording the decision as an
// entry recorded at now, with its effects. next is the stream's state after
// it; finish returns what was recorded, once the batch has run.
func (a *Aggregate[S, M, R, Ev, Ef]) queueEntry(b *Batch, l *locks, at Loaded[S], msg M, o options, meta Meta, now time.Time) (finish func() Handled[S, R], next Loaded[S], entry *Entry, err error) {
	decision := a.Decider.Decide(at.State, msg)
	if decision.Refused() && (len(decision.Events) > 0 || len(decision.Effects) > 0) {
		return nil, at, nil, fmt.Errorf("cairn: %s refused %s but returned events or effects", at.Stream, decision.Refusal.Code)
	}

	entry = &Entry{Refusal: decision.Refusal, IdempotencyKey: o.key, Meta: meta}
	if entry.Message, err = a.Messages.Encode(msg); err != nil {
		return nil, at, nil, err
	}
	if entry.Events, err = a.Events.EncodeAll(decision.Events); err != nil {
		return nil, at, nil, err
	}
	if entry.Effects, err = a.Effects.EncodeAll(decision.Effects); err != nil {
		return nil, at, nil, err
	}
	if !decision.Refused() {
		if entry.Result, err = json.Marshal(decision.Result); err != nil {
			return nil, at, nil, err
		}
	}

	if err := l.queueAppend(b, at.Stream, entry, now); err != nil {
		return nil, at, nil, err
	}
	next = at
	next.State = a.Decider.Fold(at.State, decision.Events)
	next.Seq = entry.Seq

	return func() Handled[S, R] {
		return Handled[S, R]{Entry: *entry, State: next.State, Result: decision.Result, Refusal: decision.Refusal}
	}, next, entry, nil
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
