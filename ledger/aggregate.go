package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

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

// Handle decides msg against the stream id and records the result, in its own
// transaction. A refusal is recorded and committed, then returned as the error
// (a *cairn.Refusal) alongside the populated Handled.
func (a *Aggregate[S, M, R, Ev, Ef]) Handle(ctx context.Context, id string, msg M, opts ...Option) (Handled[S, R], error) {
	var h Handled[S, R]
	err := a.Store.Transact(ctx, func(tx *Tx) error {
		stream := a.Stream(id)
		if err := tx.Lock(ctx, stream); err != nil {
			return err
		}
		at, err := a.LoadLocked(ctx, tx, id)
		if err != nil {
			return err
		}
		h, err = a.Apply(ctx, tx, &at, msg, opts...)
		return err
	})
	if err != nil {
		return Handled[S, R]{}, err
	}
	if h.Refusal != nil {
		return h, h.Refusal
	}
	return h, nil
}

// Load reads a stream's current state without locking it.
func (a *Aggregate[S, M, R, Ev, Ef]) Load(ctx context.Context, id string) (Loaded[S], error) {
	var at Loaded[S]
	err := pgx.BeginTxFunc(ctx, a.Store.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(tx pgx.Tx) error {
			var seq int64
			err := tx.QueryRow(ctx, `SELECT seq FROM cairn.streams WHERE stream = $1`, a.Stream(id).String()).Scan(&seq)
			if errors.Is(err, pgx.ErrNoRows) {
				at = Loaded[S]{Stream: a.Stream(id), State: a.Decider.Initial()}
				return nil
			}
			if err != nil {
				return err
			}
			at, err = a.load(ctx, tx, id, seq)
			return err
		})
	return at, err
}

// LoadLocked reads the state of a stream the transaction has locked.
func (a *Aggregate[S, M, R, Ev, Ef]) LoadLocked(ctx context.Context, tx *Tx, id string) (Loaded[S], error) {
	seq, err := tx.seq(a.Stream(id))
	if err != nil {
		return Loaded[S]{}, err
	}
	return a.load(ctx, tx, id, seq)
}

func (a *Aggregate[S, M, R, Ev, Ef]) load(ctx context.Context, q querier, id string, seq int64) (Loaded[S], error) {
	stream := a.Stream(id)
	at := Loaded[S]{Stream: stream, State: a.Decider.Initial(), Seq: seq}

	if a.StateVersion != "" {
		var (
			snapSeq int64
			data    []byte
		)
		err := q.QueryRow(ctx, `SELECT seq, state FROM cairn.state_snapshots WHERE stream = $1 AND version = $2`,
			stream.String(), a.StateVersion).Scan(&snapSeq, &data)
		switch {
		case err == nil && snapSeq <= seq:
			var state S
			if err := json.Unmarshal(data, &state); err != nil {
				return Loaded[S]{}, fmt.Errorf("cairn: decoding snapshot of %s: %w", stream, err)
			}
			at.State, at.snapshotSeq = state, snapSeq
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return Loaded[S]{}, fmt.Errorf("cairn: reading snapshot of %s: %w", stream, err)
		}
	}

	entries, err := readStream(ctx, q, stream, at.snapshotSeq)
	if err != nil {
		return Loaded[S]{}, err
	}
	for _, e := range entries {
		if e.Seq > seq {
			break
		}
		events, err := a.Events.DecodeAll(e.Events)
		if err != nil {
			return Loaded[S]{}, fmt.Errorf("cairn: %s#%d: %w", stream, e.Seq, err)
		}
		at.State = a.Decider.Fold(at.State, events)
	}
	return at, nil
}

// Apply decides msg against a locked, loaded stream and records the result in
// tx, updating at to the state after it. A refusal is not an error here: it is
// recorded like any other decision and reported in Handled.Refusal, so a
// caller composing several streams decides for itself whether to commit.
func (a *Aggregate[S, M, R, Ev, Ef]) Apply(ctx context.Context, tx *Tx, at *Loaded[S], msg M, opts ...Option) (Handled[S, R], error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	// A retry gets its first answer even when it lost the request's metadata:
	// the message was attributed when it was recorded.
	if o.key != "" {
		if h, found, err := a.duplicate(ctx, tx, at, o.key); err != nil || found {
			return h, err
		}
	}
	meta, err := a.meta(ctx, msg, o)
	if err != nil {
		return Handled[S, R]{}, err
	}

	decision := a.Decider.Decide(at.State, msg)
	if decision.Refused() && (len(decision.Events) > 0 || len(decision.Effects) > 0) {
		return Handled[S, R]{}, fmt.Errorf("cairn: %s refused %s but returned events or effects", at.Stream, decision.Refusal.Code)
	}

	entry := Entry{Refusal: decision.Refusal, IdempotencyKey: o.key, Meta: meta}
	if entry.Message, err = a.Messages.Encode(msg); err != nil {
		return Handled[S, R]{}, err
	}
	if entry.Events, err = a.Events.EncodeAll(decision.Events); err != nil {
		return Handled[S, R]{}, err
	}
	if entry.Effects, err = a.Effects.EncodeAll(decision.Effects); err != nil {
		return Handled[S, R]{}, err
	}
	if !decision.Refused() {
		if entry.Result, err = json.Marshal(decision.Result); err != nil {
			return Handled[S, R]{}, err
		}
	}

	if entry, err = tx.append(ctx, at.Stream, entry); err != nil {
		return Handled[S, R]{}, err
	}
	at.State = a.Decider.Fold(at.State, decision.Events)
	at.Seq = entry.Seq

	if err := a.maybeSnapshot(ctx, tx, at); err != nil {
		return Handled[S, R]{}, err
	}
	if len(decision.Events) > 0 {
		if err := a.writeViews(ctx, tx, at, entry); err != nil {
			return Handled[S, R]{}, err
		}
	}

	return Handled[S, R]{Entry: entry, State: at.State, Result: decision.Result, Refusal: decision.Refusal}, nil
}

func (a *Aggregate[S, M, R, Ev, Ef]) duplicate(ctx context.Context, tx *Tx, at *Loaded[S], key string) (Handled[S, R], bool, error) {
	rows, err := tx.Query(ctx, `SELECT `+entryColumns+` FROM cairn.entries WHERE stream = $1 AND idempotency_key = $2`,
		at.Stream.String(), key)
	if err != nil {
		return Handled[S, R]{}, false, err
	}
	entries, err := scanEntries(rows)
	if err != nil || len(entries) == 0 {
		return Handled[S, R]{}, false, err
	}
	e := entries[0]
	h := Handled[S, R]{Entry: e, State: at.State, Refusal: e.Refusal, Duplicate: true}
	if len(e.Result) > 0 {
		if err := json.Unmarshal(e.Result, &h.Result); err != nil {
			return Handled[S, R]{}, false, fmt.Errorf("cairn: decoding result of %s#%d: %w", e.Stream, e.Seq, err)
		}
	}
	return h, true, nil
}

func (a *Aggregate[S, M, R, Ev, Ef]) maybeSnapshot(ctx context.Context, tx *Tx, at *Loaded[S]) error {
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
	if _, err := tx.Exec(ctx, `
INSERT INTO cairn.state_snapshots (stream, seq, version, state) VALUES ($1, $2, $3, $4)
ON CONFLICT (stream, version) DO UPDATE SET seq = excluded.seq, state = excluded.state, taken_at = now()`,
		at.Stream.String(), at.Seq, a.StateVersion, data); err != nil {
		return fmt.Errorf("cairn: saving snapshot of %s: %w", at.Stream, err)
	}
	at.snapshotSeq = at.Seq
	return nil
}
