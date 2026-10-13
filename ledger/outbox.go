package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anyweez/cairn/codec"
)

// Delivery is one effect to carry out.
type Delivery[Ef any] struct {
	ID      int64
	Stream  string
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

	// Batch is how many effects one claim takes. Defaults to 20.
	Batch int
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
	c := dispatchConfig{batch: d.Batch, lease: d.Lease, backoff: d.Backoff}
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
    RETURNING id, stream, seq, idx, effect, attempts)
SELECT c.id, c.stream, c.seq, c.idx, c.effect, c.attempts,
    coalesce(e.actor, ''), coalesce(e.via, ''), coalesce(e.correlation_id, ''), coalesce(e.causation_id, '')
FROM claimed c JOIN cairn.entries e ON e.stream = c.stream AND e.seq = c.seq
ORDER BY c.id`,
		d.Kind, cfg.batch, cfg.lease.Seconds())
	if err != nil {
		return 0, fmt.Errorf("cairn: claiming effects: %w", err)
	}
	type claimed struct {
		id       int64
		stream   string
		seq      int64
		idx      int
		effect   []byte
		attempts int
		meta     Meta
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.id, &c.stream, &c.seq, &c.idx, &c.effect, &c.attempts,
			&c.meta.Actor, &c.meta.Via, &c.meta.CorrelationID, &c.meta.CausationID); err != nil {
			rows.Close()
			return 0, err
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
	const fenced = ` AND attempts = $2 AND done_at IS NULL AND failed_at IS NULL`
	for _, c := range batch {
		if ctx.Err() != nil {
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
				`UPDATE cairn.outbox SET failed_at = now(), last_error = $3 WHERE id = $1`+fenced, c.id, c.attempts, err.Error())
		default:
			_, err = d.Store.pool.Exec(record, `UPDATE cairn.outbox
SET available_at = now() + make_interval(secs => $3), last_error = $4 WHERE id = $1`+fenced,
				c.id, c.attempts, cfg.backoff(c.attempts).Seconds(), err.Error())
		}
		if err != nil {
			return len(batch), err
		}
	}
	return len(batch), nil
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
// unless OnError is set.
func (d *Dispatcher[Ef]) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			n, err := d.DrainOnce(ctx)
			if err != nil && ctx.Err() == nil {
				if d.OnError == nil {
					return err
				}
				d.OnError(err)
			}
			if err != nil || n == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
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
