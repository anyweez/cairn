package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
)

// Aggregate binds a Decider to the ledger: one kind of stream, its codecs,
// and how its state is snapshotted. Every method checks the aggregate with
// Validate before it does anything.
type Aggregate[S, M, R, Ev, Ef any] struct {
	Store   *Store
	Kind    string
	Decider cairn.Decider[S, M, R, Ev, Ef]

	// Codecs are how messages, results, events and effects are stored. A
	// retry decodes its stored result through Results' upcasters, and Verify
	// compares only the fields the upcast stored result carries (see Verify).
	Codecs *codec.Set[M, R, Ev, Ef]

	// StateVersion names the fold a snapshot caches. Change it whenever S
	// changes shape, and whenever Evolve changes how it computes state from
	// events, even if S is unchanged: a snapshot is the state an earlier
	// Evolve produced at a seq, and a different fold that reuses it leaves
	// that stream on the old state while a stream rebuilt from events gets the
	// new one. Snapshots of any other version are ignored and state is rebuilt
	// from events. Each version keeps its own snapshot, so processes running
	// different versions side by side don't evict each other's; those of
	// retired versions can be deleted. Empty disables snapshots. It keys
	// snapshots together with the stream name, so it must be text the ledger
	// can store and at most 1024 bytes; Validate refuses anything else.
	StateVersion string
	// SnapshotEvery is how many entries may accumulate past the last snapshot
	// before another is taken. Defaults to 100.
	SnapshotEvery int64

	// Views are written in the same transaction as every message that
	// produces events.
	Views []View[S]

	// EntryViews are written once for each entry with events, in the same
	// transaction, after Views.
	EntryViews []EntryView[S, Ev]

	// Actor, when set, returns the actor a message itself names (its "by"
	// field), which then fills Meta.Actor. A message whose metadata names a
	// different actor is not recorded, so the two can never disagree.
	Actor func(M) string

	// StreamOf, when set, returns the ID of the stream a message itself names
	// (the entity's own ID, carried in the message). A message that names one
	// is recorded only on that stream: a store that carries the same entity
	// under two identifiers would otherwise let a message about one be recorded
	// on another. Handle and Apply refuse the message with ErrStreamMismatch
	// before it is decided or answered from an idempotency key, and nothing is
	// recorded. A message that names no stream returns the empty string and may
	// be recorded on any.
	StreamOf func(M) string
}

// Stream names one of this aggregate's streams.
func (a *Aggregate[S, M, R, Ev, Ef]) Stream(id string) Stream { return Stream{Kind: a.Kind, ID: id} }

// Validate reports the first thing wrong with how the aggregate is set up: no
// store, a kind that is empty, contains a slash or cannot be stored, a state
// version the ledger cannot store or index, a Decider missing a function, a
// codec missing from Codecs, or a view or entry view whose name is empty or
// repeats another's, or whose Write is nil. Every other
// method runs it first, so a mistake is an error rather than a nil-pointer
// panic on the first message; call it at startup to fail before then.
func (a *Aggregate[S, M, R, Ev, Ef]) Validate() error {
	fail := func(what string) error { return fmt.Errorf("cairn: aggregate %s: %s", quoteText(a.Kind), what) }
	switch {
	case a.Store == nil:
		return fail("Store is nil")
	case a.Kind == "" || strings.Contains(a.Kind, "/"):
		return fail("Kind must be non-empty and contain no slash")
	case !cairn.StorableText(a.Kind):
		// Rebuild, RebuildEntries, Streams and ReadAll pass the Kind to a text
		// parameter without building a stream name, so an unstorable one would
		// otherwise reach the driver and come back as a raw SQLSTATE.
		return validStoredText(fmt.Sprintf("aggregate %s's Kind", quoteText(a.Kind)), a.Kind)
	case a.Decider.Initial == nil || a.Decider.Decide == nil || a.Decider.Evolve == nil:
		return fail("Decider needs Initial, Decide and Evolve")
	case a.Codecs == nil:
		return fail("Codecs is nil")
	case a.Codecs.Messages == nil:
		return fail("Codecs.Messages is nil")
	case a.Codecs.Results == nil:
		return fail("Codecs.Results is nil")
	case a.Codecs.Events == nil:
		return fail("Codecs.Events is nil")
	case a.Codecs.Effects == nil:
		return fail("Codecs.Effects is nil")
	}
	// The version is half the primary key of cairn.state_snapshots, so it is
	// bounded like every other text value cairn puts in a btree index. It is
	// also written into every snapshot row and read back to find them, so a
	// NUL or invalid UTF-8 is refused here rather than by the driver.
	if a.StateVersion != "" {
		if err := validIndexedText(fmt.Sprintf("the snapshot state version %s of aggregate %s", quoteText(a.StateVersion), quoteText(a.Kind)), a.StateVersion); err != nil {
			return err
		}
	}
	// Batch attributes a failing statement to the view that queued it by name,
	// so a view needs one of its own.
	names := map[string]bool{}
	for _, v := range a.Views {
		if v.Name == "" {
			return fail("a view has no Name")
		}
		if names[v.Name] {
			return fail("two views are named " + v.Name)
		}
		names[v.Name] = true
		if v.Write == nil {
			return fail("view " + v.Name + " has no Write")
		}
	}
	for _, v := range a.EntryViews {
		if v.Name == "" {
			return fail("an entry view has no Name")
		}
		if names[v.Name] {
			return fail("two views are named " + v.Name)
		}
		names[v.Name] = true
		if v.Write == nil {
			return fail("entry view " + v.Name + " has no Write")
		}
	}
	return nil
}

// Loaded is a stream's state as of a sequence number.
type Loaded[S any] struct {
	Stream Stream
	State  S
	Seq    int64

	snapshotSeq int64
	// snapshotBytes is the size of the state's last snapshot, which estimates
	// what it costs the store's cache (see StateCacheBytes). It is zero until
	// a snapshot has been written or read.
	snapshotBytes int
}

// Handled is what recording one message produced. The stream's state after
// it is not returned: what a caller needs from a decision belongs in R, and
// Load reads the state.
type Handled[R any] struct {
	Entry Entry
	// Result is the accepted decision's result as it was stored: the first
	// call, the stored entry and every idempotent retry all return this same
	// value, even when R does not survive a JSON round trip unchanged. It is
	// the zero R for a refusal: a refusal is stored without a result, and a
	// retry is answered from that entry, so the first call must answer with it
	// too.
	Result  R
	Refusal *cairn.Refusal
	// Duplicate is true when the idempotency key matched an earlier entry:
	// nothing was decided or recorded, and Result and Refusal are the earlier
	// entry's.
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
// returned. A new key is stored in a btree index, so it must be text the ledger
// can store — valid UTF-8, without a NUL — and at most 1024 bytes. Handle and
// Apply refuse any other new key before it reaches Postgres. A key recorded
// before that bound existed is still looked up and answered, so a retry
// spanning its introduction gets its first answer.
func IdempotencyKey(key string) Option { return func(o *options) { o.key = key } }

// collect applies opts and refuses a key the ledger cannot store. A key is
// passed to a text parameter both when it is looked up and when it is written,
// so one with a NUL or invalid UTF-8 would otherwise come back from the driver
// as a raw SQLSTATE. Its length is checked only when a new entry writes it
// (queueAppend): a key stored before that bound existed must still be found by
// its retry.
func collect(opts []Option) (options, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.key != "" {
		if err := validStoredText("the idempotency key "+quoteText(o.key), o.key); err != nil {
			return o, err
		}
	}
	return o, nil
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
func (a *Aggregate[S, M, R, Ev, Ef]) Handle(ctx context.Context, id string, msg M, opts ...Option) (Handled[R], error) {
	if err := a.Validate(); err != nil {
		return Handled[R]{}, err
	}
	o, err := collect(opts)
	if err != nil {
		return Handled[R]{}, err
	}
	r := &request[M, R]{ctx: ctx, msg: msg, o: o}
	r.meta, r.metaErr = a.meta(ctx, msg, o)

	var res result[R]
	if a.Store.cache != nil {
		res = a.handleCached(ctx, a.Stream(id), r)
	} else if results, _, _, err := a.runGroup(ctx, a.Stream(id), []*request[M, R]{r}, nil); err != nil {
		res.err = err
	} else {
		res = results[0]
	}
	if res.err != nil {
		return Handled[R]{}, res.err
	}
	if res.h.Refusal != nil {
		return res.h, res.h.Refusal
	}
	return res.h, nil
}

// request is one message on its way to being recorded.
type request[M, R any] struct {
	ctx     context.Context
	msg     M
	o       options
	meta    Meta
	metaErr error
	done    chan result[R]
}

type result[R any] struct {
	h   Handled[R]
	err error
}

// errUnchecked answers a message that could not be decided without reading
// the stream first: one whose metadata is unusable may still be a retry,
// which is answered from its first entry.
var errUnchecked = errors.New("cairn: the message needs its idempotency key looked up")

// answeredFromFirstEntry reports whether the request's own metadata leaves it
// eligible for the answer its idempotency key already holds. Only a missing
// actor does: the retry lost the request's context, and the message was
// attributed when it was recorded. Metadata that names a different actor is a
// contradiction, and the request is refused rather than answered. Whether an
// eligible request names the actor the entry was recorded under is a second
// question, asked of the entry itself by mismatchedActor.
func answeredFromFirstEntry(metaErr error) bool {
	return metaErr == nil || errors.Is(metaErr, ErrNoActor)
}

// mismatchedActor returns ErrActorMismatch when the request names an actor and
// the entry its idempotency key holds was recorded under a different one. An
// entry's answer belongs to the actor that recorded it, so only a retry that
// carries no actor (or the same one) may be answered from it. A request that
// names no actor is treated as a retry that lost its context, and is answered
// from the first entry.
func mismatchedActor(meta Meta, e Entry) error {
	if meta.Actor != "" && meta.Actor != e.Meta.Actor {
		return fmt.Errorf("%w: the entry was recorded by %q, the request names %q", ErrActorMismatch, e.Meta.Actor, meta.Actor)
	}
	return nil
}

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
// database reported it (see retryable), or when a caller's own handler failed
// before anything was sent. The one exception is a handler that fails in Go
// reading a result from the batch that ends the transaction: it may already
// have committed, and the error then wraps ErrRecorded. Each request's own
// result says whether its message was refused, a duplicate, or failed before
// it was sent.
func (a *Aggregate[S, M, R, Ev, Ef]) runGroup(ctx context.Context, stream Stream, reqs []*request[M, R], cached *Loaded[S]) (results []result[R], before, after Loaded[S], err error) {
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
		// One batch, which Postgres runs as one transaction the batch opens
		// and commits itself.
		b.own(sqlStep("beginning a transaction"), `BEGIN`)
		if l, err = queueLockAt(&b, stream, cached.Seq); err != nil {
			return nil, Loaded[S]{}, Loaded[S]{}, err
		}
		at = *cached
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

	results = make([]result[R], len(reqs))
	finishes := make([]func() Handled[R], len(reqs))
	// cause is the last entry with events, and viewAt the state it left: the
	// entries after it changed nothing a view shows, and a view is written as
	// of the entry that caused it, as Rebuild writes it.
	var (
		cause  *Entry
		viewAt Loaded[S]
		// entryViews are the entry views' writes, sent after the views.
		entryViews Batch
	)
	for i, r := range reqs {
		switch {
		// A request whose metadata names a different actor is refused even
		// when its idempotency key matches: the entry it names is another
		// actor's answer.
		case !answeredFromFirstEntry(r.metaErr):
			results[i].err = r.metaErr
			continue
		// A retry that lost its metadata may still be answered from its first
		// entry, so its key is looked up before it is decided. Without the
		// cache that lookup has already happened, in dups.
		case r.metaErr != nil && r.o.key != "" && cached != nil:
			results[i].err = errUnchecked
			continue
		// A retry gets its first answer even when it lost the request's
		// metadata: the message was attributed when it was recorded. A
		// request that an identity guard refuses is refused here too, rather
		// than answered: the entry's answer belongs to the actor and stream
		// that recorded it.
		case dups[i] != nil && dups[i].found:
			results[i].h, results[i].err = a.duplicateAnswer(r.msg, r.meta, dups[i].entry, stream)
			continue
		case r.metaErr != nil:
			results[i].err = r.metaErr
			continue
		}
		// Each message's statements join the batch only once all of them are
		// queued, so one that fails leaves nothing of itself behind.
		var one, ev Batch
		seq := l.seqs[stream.String()]
		finish, next, entry, events, err := a.queueEntry(&one, l, at, r.msg, r.o, r.meta, l.at)
		if err == nil && len(events) > 0 {
			err = a.queueEntryViews(&ev, at, next, *entry, events)
		}
		if err != nil {
			l.seqs[stream.String()] = seq
			results[i].err = err
			if r.o.key != "" && cached != nil {
				// A retry is answered from its first entry, which only the
				// stored stream can show.
				results[i].err = errUnchecked
			}
			continue
		}
		b.merge(&one)
		entryViews.merge(&ev)
		if len(entry.Events) > 0 {
			cause, viewAt = entry, next
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
		if err := a.queueViews(&b, viewAt, *cause); err != nil {
			return nil, Loaded[S]{}, Loaded[S]{}, err
		}
	}
	b.merge(&entryViews)
	b.end()
	if err := b.send(ctx, conn); err != nil {
		return nil, Loaded[S]{}, Loaded[S]{}, viewError(err, viewAt)
	}
	for i, finish := range finishes {
		if finish != nil {
			results[i].h = finish()
		}
	}
	return results, before, at, nil
}

func anyRecorded[R any](finishes []func() Handled[R]) bool {
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
	if err := a.Validate(); err != nil {
		return Loaded[S]{}, err
	}
	stream := a.Stream(id)
	if err := validStream(stream); err != nil {
		return Loaded[S]{}, err
	}
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
	// Load records nothing, so a handler failing in Go is never ErrRecorded:
	// it queues its COMMIT with own, leaving commits and hasCommit unset.
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
	if err := a.Validate(); err != nil {
		return Loaded[S]{}, err
	}
	stream := a.Stream(id)
	seq, err := tx.locks.seq(stream)
	if err != nil {
		return Loaded[S]{}, err
	}
	var b Batch
	load := a.queueLoad(&b, stream)
	if err := b.send(ctx, tx.tx); err != nil {
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
		at.State, at.snapshotSeq, at.snapshotBytes = state, p.snapSeq, len(p.snapshot)
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
		events, err := a.Codecs.Events.DecodeAll(e.Events)
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
// caller composing several streams decides for itself whether to commit. The
// usual check of err therefore passes over a refusal; read Handled.Refusal, or
// the transaction commits past it. Handle, by contrast, returns the refusal as
// its error.
//
// It costs one round trip, and one more to look up an idempotency key.
func (a *Aggregate[S, M, R, Ev, Ef]) Apply(ctx context.Context, tx *Tx, at *Loaded[S], msg M, opts ...Option) (Handled[R], error) {
	if err := a.Validate(); err != nil {
		return Handled[R]{}, err
	}
	o, err := collect(opts)
	if err != nil {
		return Handled[R]{}, err
	}
	// Resolve the metadata before the key is looked up, so a request that
	// names an actor its metadata contradicts is refused rather than answered
	// from that actor's entry.
	meta, metaErr := a.meta(ctx, msg, o)
	// A retry gets its first answer even when it lost the request's metadata:
	// the message was attributed when it was recorded.
	if o.key != "" {
		var b Batch
		dup := queueDuplicate(&b, at.Stream, o.key)
		if err := b.send(ctx, tx.tx); err != nil {
			return Handled[R]{}, err
		}
		if dup.found {
			if !answeredFromFirstEntry(metaErr) {
				return Handled[R]{}, metaErr
			}
			return a.duplicateAnswer(msg, meta, dup.entry, at.Stream)
		}
	}
	if metaErr != nil {
		return Handled[R]{}, metaErr
	}

	var b Batch
	seq, notLocked := tx.locks.seq(at.Stream)
	// Every entry in one transaction shares its recorded_at, taken at Lock.
	var recordedAt time.Time
	if tx.locks != nil {
		recordedAt = tx.locks.at
	}
	finish, next, entry, events, err := a.queueEntry(&b, tx.locks, *at, msg, o, meta, recordedAt)
	if err == nil {
		err = a.queueSnapshot(&b, &next)
	}
	if err == nil && len(entry.Events) > 0 {
		err = a.queueViews(&b, next, *entry)
	}
	if err == nil && len(events) > 0 {
		err = a.queueEntryViews(&b, *at, next, *entry, events)
	}
	if err != nil {
		// Nothing was sent, so the transaction can go on as if Apply hadn't
		// been called.
		if notLocked == nil {
			tx.locks.seqs[at.Stream.String()] = seq
		}
		return Handled[R]{}, err
	}
	if err := b.send(ctx, tx.tx); err != nil {
		return Handled[R]{}, viewError(err, next)
	}
	*at = next
	return finish(), nil
}

// ErrStreamMismatch is returned for a message that names a stream other than
// the one it is being recorded on. See Aggregate.StreamOf.
var ErrStreamMismatch = errors.New("cairn: the message names a stream it is not recorded on")

// streamMismatch returns ErrStreamMismatch when msg names a stream other than
// stream. A message that names none may be recorded on any.
func (a *Aggregate[S, M, R, Ev, Ef]) streamMismatch(msg M, stream Stream) error {
	if a.StreamOf == nil {
		return nil
	}
	if named := a.StreamOf(msg); named != "" && named != stream.ID {
		return fmt.Errorf("%w: %s names %s", ErrStreamMismatch, stream, quoteText(named))
	}
	return nil
}

// queueEntry decides msg against at and queues recording the decision as an
// entry recorded at now, with its effects. next is the stream's state after
// it; finish returns what was recorded, once the batch has run. events are
// the decision's.
func (a *Aggregate[S, M, R, Ev, Ef]) queueEntry(b *Batch, l *locks, at Loaded[S], msg M, o options, meta Meta, now time.Time) (finish func() Handled[R], next Loaded[S], entry *Entry, events []Ev, err error) {
	if err := a.streamMismatch(msg, at.Stream); err != nil {
		return nil, at, nil, nil, err
	}
	// The entry is the record of what the caller sent, so the message is
	// encoded before Decide sees it: Go copies the message by value, but a
	// slice, map or pointer field is a header shared with the caller, so a
	// decider that normalises one in place — sort.Ints(m.Order), say — would
	// otherwise change both the bytes this entry stores and the caller's own
	// value.
	stored, encodeErr := a.Codecs.Messages.Encode(msg)
	// Decide is given the message decoded back from that envelope, which is the
	// message a replay reads: Verify decodes the entry and re-decides it, so a
	// decision and its replay cannot disagree about the message they were made
	// against. A message that cannot be encoded has no such copy; Decide is
	// then given it as it was handed over, and nothing is recorded (see
	// below), so a write through a field reaches no entry.
	decided := msg
	if encodeErr == nil {
		if decided, err = a.Codecs.Messages.Decode(stored); err != nil {
			return nil, at, nil, nil, err
		}
	}
	decision := a.Decider.Decide(at.State, decided)
	if decision.Refused() && (len(decision.Events) > 0 || len(decision.Effects) > 0) {
		return nil, at, nil, nil, fmt.Errorf("cairn: %s refused %s but returned events or effects", at.Stream, decision.Refusal.Code)
	}

	entry = &Entry{Refusal: decision.Refusal, IdempotencyKey: o.key, Meta: meta, Message: stored}
	if encodeErr != nil {
		// The message cannot be stored at all, so a refusal of it cannot be
		// recorded either. Return the decider's refusal rather than the
		// encoding error: the caller's message was answered, and an entry that
		// cannot hold the message would be worse than none.
		var notStorable *codec.NotStorableError
		if decision.Refused() && errors.As(encodeErr, &notStorable) {
			return nil, at, nil, nil, decision.Refusal
		}
		return nil, at, nil, nil, encodeErr
	}
	if entry.Events, err = a.Codecs.Events.EncodeAll(decision.Events); err != nil {
		return nil, at, nil, nil, err
	}
	if entry.Effects, err = a.Codecs.Effects.EncodeAll(decision.Effects); err != nil {
		return nil, at, nil, nil, err
	}
	if decision.Refused() {
		// A refusal is not carried by a codec, so its text is checked here,
		// the way Encode checks a message's. A code that is not valid UTF-8
		// would no longer be what Verify compares and errors.Is matches, and a
		// NUL cannot be represented in jsonb at all; either answers the
		// decider's refusal without an entry, rather than recording text that
		// no longer says what it said. The refusal's message may be invalid
		// UTF-8: it is stored as the U+FFFD json.Marshal substitutes, and the
		// refusal returned is that stored text rather than the text before the
		// rewrite.
		if !cairn.StorableText(entry.Refusal.Code) || strings.ContainsRune(entry.Refusal.Message, 0) {
			return nil, at, nil, nil, decision.Refusal
		}
		if entry.Refusal, err = storableRefusal(entry.Refusal); err != nil {
			return nil, at, nil, nil, err
		}
	} else {
		result, err := a.Codecs.Results.Encode(decision.Result)
		if err != nil {
			return nil, at, nil, nil, err
		}
		// The first answer is the stored one. A retry decodes the entry's
		// result, so a result that does not survive the round trip unchanged
		// would make the first call and every retry disagree. Decoding here
		// also refuses a result that cannot be read back before anything is
		// appended.
		if decision.Result, err = a.decodeResult(result); err != nil {
			return nil, at, nil, nil, err
		}
		entry.Result = &result
	}

	if err := l.queueAppend(b, at.Stream, entry, now); err != nil {
		return nil, at, nil, nil, err
	}
	next = at
	next.State = a.Decider.Fold(at.State, decision.Events)
	next.Seq = entry.Seq

	return func() Handled[R] {
		// A refusal stores no result (Entry.Result is nil when the message was
		// refused), so return the zero R rather than the decider's. An accepted
		// decision.Result was decoded from the stored envelope above, so this is
		// the value a retry reads back.
		var result R
		if !decision.Refused() {
			result = decision.Result
		}
		return Handled[R]{Entry: *entry, Result: result, Refusal: entry.Refusal}
	}, next, entry, decision.Events, nil
}

// storableRefusal returns r as the ledger will read it back. json.Marshal
// rewrites a string that is not valid UTF-8 to U+FFFD, so a round trip through
// it makes the refusal Handle returns the same text a later read or an
// idempotent retry sees. It is not a storable-text check: that is the caller's,
// since a refusal carrying a NUL or an unstorable code must not be recorded at
// all.
func storableRefusal(r *cairn.Refusal) (*cairn.Refusal, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	out := &cairn.Refusal{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
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

// duplicateAnswer answers a request from the entry its idempotency key holds,
// unless an identity guard that would refuse the message refuses it first: the
// entry's answer belongs to the actor and stream that recorded it, so a request
// Handle or Apply would refuse is never answered from a key.
func (a *Aggregate[S, M, R, Ev, Ef]) duplicateAnswer(msg M, meta Meta, e Entry, stream Stream) (Handled[R], error) {
	if err := mismatchedActor(meta, e); err != nil {
		return Handled[R]{}, err
	}
	if err := a.streamMismatch(msg, stream); err != nil {
		return Handled[R]{}, err
	}
	return a.duplicateOf(e)
}

// duplicateOf answers a retry with the entry that first answered it.
func (a *Aggregate[S, M, R, Ev, Ef]) duplicateOf(e Entry) (Handled[R], error) {
	h := Handled[R]{Entry: e, Refusal: e.Refusal, Duplicate: true}
	if e.Result != nil {
		var err error
		if h.Result, err = a.decodeResult(*e.Result); err != nil {
			return Handled[R]{}, fmt.Errorf("cairn: decoding result of %s#%d: %w", e.Stream, e.Seq, err)
		}
	}
	return h, nil
}

// decodeResult decodes a stored result through Results.
func (a *Aggregate[S, M, R, Ev, Ef]) decodeResult(env codec.Envelope) (R, error) {
	return a.Codecs.Results.Decode(env)
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
	if a.StateVersion == "" {
		return nil
	}
	// A snapshot is read back with json.Unmarshal, so it must hold exactly the
	// state that was stored. CheckStorable refuses the text json.Marshal would
	// otherwise rewrite - a string that is not valid UTF-8 becomes U+FFFD, and
	// a NUL cannot be stored at all - so a state that would come back different
	// is refused here, naming the field, instead of being recorded.
	if err := codec.CheckStorable(at.State); err != nil {
		return fmt.Errorf("cairn: the snapshot state of %s cannot be stored: %w", at.Stream, err)
	}
	data, err := json.Marshal(at.State)
	if err != nil {
		return fmt.Errorf("cairn: encoding snapshot of %s: %w", at.Stream, err)
	}
	b.own(sqlStep("saving snapshot of "+at.Stream.String()), `
INSERT INTO cairn.state_snapshots (stream, seq, version, state) VALUES ($1, $2, $3, $4)
ON CONFLICT (stream, version) DO UPDATE SET seq = excluded.seq, state = excluded.state, taken_at = now()`,
		at.Stream.String(), at.Seq, a.StateVersion, jsonArg(data))
	at.snapshotSeq = at.Seq
	at.snapshotBytes = len(data)
	return nil
}
