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
	Handle  func(ctx context.Context, d Delivery[Ef]) error

	// Batch is how many effects one claim takes. Defaults to 20.
	Batch int
	// Lease is how long a claimed effect is hidden from other dispatchers.
	// Defaults to a minute.
	Lease time.Duration
	// Backoff is the delay before retrying a failed attempt. Defaults to
	// doubling from a second, capped at ten minutes.
	Backoff func(attempt int) time.Duration
	// MaxAttempts gives up on an effect after this many failures, leaving it
	// marked failed for an operator. Zero retries forever.
	MaxAttempts int
}

func (d *Dispatcher[Ef]) defaults() {
	if d.Batch <= 0 {
		d.Batch = 20
	}
	if d.Lease <= 0 {
		d.Lease = time.Minute
	}
	if d.Backoff == nil {
		d.Backoff = func(attempt int) time.Duration {
			delay := time.Second << min(attempt-1, 10)
			return min(delay, 10*time.Minute)
		}
	}
}

// DrainOnce claims one batch and carries it out, returning how many effects
// it attempted.
func (d *Dispatcher[Ef]) DrainOnce(ctx context.Context) (int, error) {
	d.defaults()
	rows, err := d.Store.pool.Query(ctx, `
UPDATE cairn.outbox SET attempts = attempts + 1, available_at = now() + make_interval(secs => $3)
WHERE id IN (
    SELECT id FROM cairn.outbox
    WHERE kind = $1 AND done_at IS NULL AND failed_at IS NULL AND available_at <= now()
    ORDER BY id
    FOR UPDATE SKIP LOCKED
    LIMIT $2)
RETURNING id, stream, seq, idx, effect, attempts`,
		d.Kind, d.Batch, d.Lease.Seconds())
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
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.id, &c.stream, &c.seq, &c.idx, &c.effect, &c.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, c := range batch {
		err := d.deliver(ctx, c.stream, c.seq, c.idx, c.id, c.effect, c.attempts)
		if err == nil {
			if _, err := d.Store.pool.Exec(ctx, `UPDATE cairn.outbox SET done_at = now(), last_error = NULL WHERE id = $1`, c.id); err != nil {
				return 0, err
			}
			continue
		}
		if d.MaxAttempts > 0 && c.attempts >= d.MaxAttempts {
			_, err = d.Store.pool.Exec(ctx, `UPDATE cairn.outbox SET failed_at = now(), last_error = $2 WHERE id = $1`, c.id, err.Error())
		} else {
			_, err = d.Store.pool.Exec(ctx, `UPDATE cairn.outbox SET available_at = now() + make_interval(secs => $2), last_error = $3 WHERE id = $1`,
				c.id, d.Backoff(c.attempts).Seconds(), err.Error())
		}
		if err != nil {
			return 0, err
		}
	}
	return len(batch), nil
}

func (d *Dispatcher[Ef]) deliver(ctx context.Context, stream string, seq int64, idx int, id int64, raw []byte, attempt int) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	var env codec.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	effect, err := d.Effects.Decode(env)
	if err != nil {
		return err
	}
	return d.Handle(ctx, Delivery[Ef]{ID: id, Stream: stream, Seq: seq, Index: idx, Effect: effect, Attempt: attempt})
}

// Run drains the outbox every interval until ctx is done.
func (d *Dispatcher[Ef]) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for {
			n, err := d.DrainOnce(ctx)
			if err != nil {
				return err
			}
			if n == 0 {
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
