package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anyweez/cairn/codec"
)

// Delivery is one effect to carry out.
type Delivery[Ef any] struct {
	ID      int64
	Stream  Stream
	Seq     int64
	Index   int
	Effect  Ef
	Attempt int
	// Meta is the metadata of the entry that produced the effect.
	Meta Meta
}

// Key identifies the effect across retries. Pass it to whatever carries the
// effect out, so a delivery repeated after a crash is recognised downstream.
func (d Delivery[Ef]) Key() string { return fmt.Sprintf("%s#%d.%d", d.Stream, d.Seq, d.Index) }

// Dispatcher carries out one kind's effects from the outbox, at least once
// each. Effects are claimed with a lease, so several dispatchers can share
// the work; order between effects is not guaranteed.
type Dispatcher[Ef any] struct {
	Store   *Store
	Kind    string
	Effects *codec.Codec[Ef]
	// Handle carries out one effect. Its context ends when the lease does, so
	// a handler that outlives its lease is cancelled rather than racing the
	// dispatcher that reclaims the effect.
	//
	// The context carries metadata for any message the handler records: the
	// producing entry's actor (or "outbox" when it had none) and correlation
	// ID, Via "outbox", and the delivery's Key as the CausationID. Replace the
	// actor with ContextWithMeta when the handler acts as itself.
	Handle func(ctx context.Context, d Delivery[Ef]) error

	// BatchSize is how many effects one claim takes. Defaults to 20.
	BatchSize int
	// Lease is how long one delivery may take before the effect is offered to
	// another dispatcher. Defaults to a minute.
	Lease time.Duration
	// Backoff is the delay before retrying a failed attempt. Defaults to
	// doubling from a second, capped at ten minutes.
	Backoff func(attempt int) time.Duration
	// MaxAttempts gives up on an effect after this many attempts, leaving it
	// marked failed for an operator. Zero retries forever.
	MaxAttempts int
	// OnError, when set, keeps Run going after an error, reporting it here and
	// trying again at the next interval. When nil, Run returns the error.
	OnError func(error)
}

type dispatchConfig struct {
	batch   int
	lease   time.Duration
	backoff func(attempt int) time.Duration
}

func (d *Dispatcher[Ef]) config() dispatchConfig {
	c := dispatchConfig{batch: d.BatchSize, lease: d.Lease, backoff: d.Backoff}
	if c.batch <= 0 {
		c.batch = 20
	}
	if c.lease <= 0 {
		c.lease = time.Minute
	}
	if c.backoff == nil {
		c.backoff = func(attempt int) time.Duration {
			delay := time.Second << min(attempt-1, 10)
			return min(delay, 10*time.Minute)
		}
	}
	return c
}

// DrainOnce claims one batch and carries it out, returning how many effects
// it claimed.
//
// Every claim increments an effect's attempts, and every later write for that
// effect is conditional on attempts being unchanged. A dispatcher whose lease
// ran out, and whose effect another dispatcher has since claimed, therefore
// cannot mark it done or failed.
func (d *Dispatcher[Ef]) DrainOnce(ctx context.Context) (int, error) {
	cfg := d.config()
	rows, err := d.Store.pool.Query(ctx, `
WITH claimed AS (
    UPDATE cairn.outbox SET attempts = attempts + 1, available_at = now() + make_interval(secs => $3)
    WHERE id IN (
        SELECT id FROM cairn.outbox
        WHERE kind = $1 AND done_at IS NULL AND failed_at IS NULL AND available_at <= now()
        ORDER BY id
        FOR UPDATE SKIP LOCKED
        LIMIT $2)
    RETURNING id, stream, kind, seq, idx, effect, attempts)
SELECT c.id, c.stream, c.kind, c.seq, c.idx, c.effect, c.attempts,
    coalesce(e.actor, ''), coalesce(e.via, ''), coalesce(e.correlation_id, ''), coalesce(e.causation_id, ''),
    e.occurred_at, e.extra
FROM claimed c JOIN cairn.entries e ON e.stream = c.stream AND e.seq = c.seq
ORDER BY c.id`,
		d.Kind, cfg.batch, cfg.lease.Seconds())
	if err != nil {
		return 0, fmt.Errorf("cairn: claiming effects: %w", err)
	}
	type claimed struct {
		id       int64
		stream   Stream
		seq      int64
		idx      int
		effect   []byte
		attempts int
		meta     Meta
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		var name, kind string
		var occurred *time.Time
		var extra []byte
		if err := rows.Scan(&c.id, &name, &kind, &c.seq, &c.idx, &c.effect, &c.attempts,
			&c.meta.Actor, &c.meta.Via, &c.meta.CorrelationID, &c.meta.CausationID, &occurred, &extra); err != nil {
			rows.Close()
			return 0, err
		}
		c.stream = parseStream(name, kind)
		if err := decodeMetaTail(&c.meta, occurred, extra); err != nil {
			rows.Close()
			return 0, fmt.Errorf("cairn: %s#%d metadata: %w", c.stream, c.seq, err)
		}
		batch = append(batch, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Outcomes are recorded even when ctx is cancelled mid-batch, so an effect
	// that was carried out is not carried out again after shutdown.
	record := context.WithoutCancel(ctx)
	// giveBack returns the effects the walk did not reach to the state the claim
	// found them in: the claim counted an attempt against every row in the
	// batch, but an attempt is spent by a delivery, so a dispatcher stopped
	// mid-batch — the ordinary shutdown — must not leave the rows behind the one
	// in flight counted, or the next drain gives up on them without ever
	// handing them out. One round trip; fenced row by row against the claim's
	// own attempt count, so an effect another dispatcher has since reclaimed is
	// left with that dispatcher's count.
	giveBack := func(rest []claimed) error {
		if len(rest) == 0 {
			return nil
		}
		ids := make([]int64, len(rest))
		atts := make([]int, len(rest))
		for i, r := range rest {
			ids[i], atts[i] = r.id, r.attempts
		}
		_, err := d.Store.pool.Exec(record, `UPDATE cairn.outbox o
SET attempts = o.attempts - 1, available_at = now()
FROM unnest($1::bigint[], $2::int[]) AS u(id, att)
WHERE o.id = u.id AND o.attempts = u.att AND o.done_at IS NULL AND o.failed_at IS NULL`, ids, atts)
		return err
	}
	const fenced = ` AND attempts = $2 AND done_at IS NULL AND failed_at IS NULL`
	for i := range batch {
		c := batch[i]
		if ctx.Err() != nil {
			giveBack(batch[i:]) // not the reason the walk stopped
			return len(batch), ctx.Err()
		}
		// Claimed more times than allowed without an outcome being recorded:
		// earlier attempts died with their dispatcher.
		if d.MaxAttempts > 0 && c.attempts > d.MaxAttempts {
			if _, err := d.Store.pool.Exec(record, `UPDATE cairn.outbox SET failed_at = now(),
last_error = coalesce(last_error, 'no outcome recorded after ' || $3::int || ' attempts') WHERE id = $1`+fenced,
				c.id, c.attempts, d.MaxAttempts); err != nil {
				return len(batch), err
			}
			continue
		}
		// The claim's lease started with the batch; restart it for this effect.
		tag, err := d.Store.pool.Exec(record, `UPDATE cairn.outbox
SET available_at = now() + make_interval(secs => $3) WHERE id = $1`+fenced, c.id, c.attempts, cfg.lease.Seconds())
		if err != nil {
			giveBack(batch[i:]) // c's update failed, so it was not handed out either
			return len(batch), err
		}
		if tag.RowsAffected() == 0 {
			continue // reclaimed by another dispatcher
		}

		handleCtx, cancel := context.WithTimeout(ctx, cfg.lease)
		err = d.deliver(handleCtx, Delivery[Ef]{ID: c.id, Stream: c.stream, Seq: c.seq, Index: c.idx,
			Attempt: c.attempts, Meta: c.meta}, c.effect)
		cancel()
		switch {
		case err == nil:
			_, err = d.Store.pool.Exec(record,
				`UPDATE cairn.outbox SET done_at = now(), last_error = NULL WHERE id = $1`+fenced, c.id, c.attempts)
		case d.MaxAttempts > 0 && c.attempts >= d.MaxAttempts:
			_, err = d.Store.pool.Exec(record,
				`UPDATE cairn.outbox SET failed_at = now(), last_error = $3 WHERE id = $1`+fenced, c.id, c.attempts, storableError(err))
		default:
			_, err = d.Store.pool.Exec(record, `UPDATE cairn.outbox
SET available_at = now() + make_interval(secs => $3), last_error = $4 WHERE id = $1`+fenced,
				c.id, c.attempts, cfg.backoff(c.attempts).Seconds(), storableError(err))
		}
		if err != nil {
			giveBack(batch[i+1:]) // c was handed out; the rows behind it were not
			return len(batch), err
		}
	}
	return len(batch), nil
}

// storableError returns err's text with anything the ledger cannot store
// replaced by the Unicode replacement rune. A handler's error comes from
// another system, so it may carry a NUL or invalid UTF-8; recording why an
// effect failed must not lose the cause to a raw driver error.
func storableError(err error) string {
	return strings.ToValidUTF8(strings.ReplaceAll(err.Error(), "\x00", "\uFFFD"), "\uFFFD")
}

func (d *Dispatcher[Ef]) deliver(ctx context.Context, dl Delivery[Ef], raw []byte) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	var env codec.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	if dl.Effect, err = d.Effects.Decode(env); err != nil {
		return err
	}
	return d.Handle(ContextWithMeta(ctx, dl.caused()), dl)
}

// Run drains the outbox every interval until ctx is done. An error ends it
// unless OnError is set. The interval must be positive.
func (d *Dispatcher[Ef]) Run(ctx context.Context, interval time.Duration) error {
	return poll(ctx, interval, d.OnError, func(ctx context.Context) (bool, error) {
		n, err := d.DrainOnce(ctx)
		return n > 0, err
	})
}

// caused is the metadata for a message recorded while carrying out d: the
// same actor and flow as the entry that produced the effect, caused by this
// delivery. A flow with no correlation ID starts at that entry. An entry with
// no actor, recorded before entries had one or by a store that does not
// require one, is attributed to the outbox, so its follow-ups are not refused
// by a store that does.
func (d Delivery[Ef]) caused() Meta {
	correlation := d.Meta.CorrelationID
	if correlation == "" {
		correlation = fmt.Sprintf("%s#%d", d.Stream, d.Seq)
	}
	actor := d.Meta.Actor
	if actor == "" {
		actor = "outbox"
	}
	return Meta{Actor: actor, Via: "outbox", CorrelationID: correlation, CausationID: d.Key()}
}
