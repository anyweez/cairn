package ledger

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgconn"
)

// StateCache keeps the state of recently used streams in memory between
// messages, for the aggregates that set it as their Cache, and lines up the
// messages each stream receives in this process.
//
// A message for a stream whose state is cached is decided as soon as it is
// its turn, and recorded in one round trip. The transaction locks the stream
// on the condition that it is still at the seq the cache holds, and writes the
// entry in the same batch, so the lock is held for no round trip at all. If
// another process has written the stream since, the condition fails, nothing
// is recorded, and the message is decided again from the stream's stored
// state. Whatever the cache holds, every decision is checked against the
// stored seq under the stream's lock before it is recorded.
//
// Messages that arrive for a stream while one of its transactions is in
// flight wait for it, and are then decided in order and recorded together, in
// one transaction. Each caller still returns only once its own message is
// committed, but one stream is no longer limited to one commit per message.
// Views and snapshots are written once per transaction, from the state after
// its last message.
//
// A cache pays off for streams whose messages mostly reach one process: route
// them by stream. Cached state is shared between messages: Decide and Evolve
// must not change the state they are given (spectest holds deciders to this),
// and the State in a Handled is the cached value itself, so treat it as
// read-only.
type StateCache struct {
	mu      sync.Mutex
	size    int
	order   *list.List // of *cachedState, most recently used first
	entries map[string]*list.Element
	lanes   map[string][]any // messages waiting, for each stream with a worker
}

type cachedState struct {
	name  string
	state any
}

// NewStateCache returns a cache holding the state of up to streams streams.
func NewStateCache(streams int) *StateCache {
	return &StateCache{
		size:    max(streams, 1),
		order:   list.New(),
		entries: map[string]*list.Element{},
		lanes:   map[string][]any{},
	}
}

func (c *StateCache) get(name string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*cachedState).state, true
}

func (c *StateCache) put(name string, state any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[name]; ok {
		e.Value.(*cachedState).state = state
		c.order.MoveToFront(e)
		return
	}
	c.entries[name] = c.order.PushFront(&cachedState{name: name, state: state})
	for c.order.Len() > c.size {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*cachedState).name)
	}
}

func (c *StateCache) drop(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[name]; ok {
		c.order.Remove(e)
		delete(c.entries, name)
	}
}

// enqueue adds item to the stream's lane. It reports whether the lane had no
// worker, in which case the caller must start one.
func (c *StateCache) enqueue(name string, item any) (start bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting, running := c.lanes[name]
	c.lanes[name] = append(waiting, item)
	return !running
}

// take removes up to n of the stream's waiting items, oldest first. When none
// are waiting it closes the lane and returns nil, and the worker must stop.
func (c *StateCache) take(name string, n int) []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting := c.lanes[name]
	if len(waiting) == 0 {
		delete(c.lanes, name)
		return nil
	}
	n = min(n, len(waiting))
	taken := append([]any(nil), waiting[:n]...)
	c.lanes[name] = waiting[n:]
	return taken
}

// groupMax is the most messages one transaction records.
const groupMax = 100

// handleCached hands r to the stream's lane and waits for its result. A
// caller whose context ends while its message is in flight returns at once,
// and the message may still be recorded, as with any write abandoned
// mid-flight.
func (a *Aggregate[S, M, R, Ev, Ef]) handleCached(ctx context.Context, stream Stream, r *request[S, M, R]) result[S, R] {
	r.done = make(chan result[S, R], 1)
	if a.Cache.enqueue(stream.String(), r) {
		go a.drain(stream)
	}
	select {
	case res := <-r.done:
		return res
	case <-ctx.Done():
		return result[S, R]{err: ctx.Err()}
	}
}

// drain records the stream's waiting messages until none are left.
func (a *Aggregate[S, M, R, Ev, Ef]) drain(stream Stream) {
	for {
		taken := a.Cache.take(stream.String(), groupMax)
		if taken == nil {
			return
		}
		var reqs []*request[S, M, R]
		for _, item := range taken {
			r := item.(*request[S, M, R])
			if err := r.ctx.Err(); err != nil {
				r.done <- result[S, R]{err: err}
				continue
			}
			reqs = append(reqs, r)
		}
		for len(reqs) > 0 {
			n := groupLen(reqs)
			a.record(stream, reqs[:n], true)
			reqs = reqs[n:]
		}
	}
}

// groupLen is how many of reqs, from the first, can share a transaction: up
// to one that repeats an idempotency key, which must find the first one
// recorded.
func groupLen[S, M, R any](reqs []*request[S, M, R]) int {
	keys := map[string]bool{}
	for i, r := range reqs {
		if r.o.key == "" {
			continue
		}
		if keys[r.o.key] {
			return i
		}
		keys[r.o.key] = true
	}
	return len(reqs)
}

// record records reqs in one transaction, from the cached state when
// fromCache is set and there is one, and delivers every result.
func (a *Aggregate[S, M, R, Ev, Ef]) record(stream Stream, reqs []*request[S, M, R], fromCache bool) {
	name := stream.String()
	ctx, stop := groupContext(reqs)
	defer stop()
	defer func() {
		// A panic in the decider would otherwise end the process from a
		// goroutine no caller can recover in.
		if p := recover(); p != nil {
			a.Cache.drop(name)
			for _, r := range reqs {
				deliver(r, result[S, R]{err: fmt.Errorf("cairn: panic recording a message to %s: %v", name, p)})
			}
		}
	}()

	var cached *Loaded[S]
	if fromCache {
		if v, ok := a.Cache.get(name); ok {
			at := v.(Loaded[S])
			cached = &at
		}
	}
	results, after, err := a.runGroup(ctx, stream, reqs, cached)
	if err != nil && cached != nil && retryable(err) {
		// Stale, or a duplicate key: nothing was recorded. Decide again from
		// the stored state.
		results, after, err = a.runGroup(ctx, stream, reqs, nil)
	}
	if err != nil {
		a.Cache.drop(name)
		if len(reqs) > 1 && retryable(err) {
			// One message spoiled the transaction for all; find which.
			for _, r := range reqs {
				a.record(stream, []*request[S, M, R]{r}, false)
			}
			return
		}
		for _, r := range reqs {
			deliver(r, result[S, R]{err: err})
		}
		return
	}

	a.Cache.put(name, after)
	for i, r := range reqs {
		if errors.Is(results[i].err, errUnchecked) {
			a.record(stream, []*request[S, M, R]{r}, false)
			continue
		}
		deliver(r, results[i])
	}
}

func deliver[S, M, R any](r *request[S, M, R], res result[S, R]) {
	select {
	case r.done <- res:
	default: // already answered
	}
}

// retryable reports whether a transaction failed in a way that recorded
// nothing, so its messages can be decided again: the database refused a
// statement, which aborts every statement after it including the commit, or
// a view failed before anything was sent. Any other failure, such as a
// connection lost while committing, leaves it unknown whether the commit
// happened, and is returned to the callers instead.
func retryable(err error) bool {
	var (
		pgErr *pgconn.PgError
		view  *viewFailure
	)
	return errors.As(err, &pgErr) || errors.As(err, &view)
}

// groupContext is a context for recording reqs together, which ends once every
// one of their callers has given up.
func groupContext[S, M, R any](reqs []*request[S, M, R]) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqs[0].ctx))
	var waiting atomic.Int64
	waiting.Store(int64(len(reqs)))
	stops := make([]func() bool, len(reqs))
	for i, r := range reqs {
		stops[i] = context.AfterFunc(r.ctx, func() {
			if waiting.Add(-1) == 0 {
				cancel()
			}
		})
	}
	return ctx, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}
