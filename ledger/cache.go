package ledger

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// A store keeps the state of recently used streams in memory between
// messages (see StateCacheSize), and lines up the messages each stream
// receives in this process.
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
// A stream that keeps going stale, because other processes write it too, is
// read from the store before each decision for a while, for longer each time
// it turns out stale, so it costs about what it would with no cache.
//
// Messages that arrive for a stream while one of its transactions is in
// flight wait for it, and are then decided in order and recorded together, in
// one transaction. Each caller still returns only once its own message is
// committed, but one stream is no longer limited to one commit per message.
// Views and snapshots are written once per transaction, from the state after
// its last message.
type stateCache struct {
	mu      sync.Mutex
	size    int
	order   *list.List // of *cachedState, most recently used first
	entries map[string]*list.Element
	lanes   map[laneKey][]any // messages waiting, for each lane with a worker
}

type cachedState struct {
	key   string
	state any // a Loaded[S]
	// misses counts the times in a row the state turned out stale; skip is
	// how many more messages to decide from the stored state first.
	misses, skip int
}

// laneKey names one aggregate's lane for one stream. Messages are only
// recorded together when they come through the same Aggregate.
type laneKey struct {
	agg  any
	name string
}

// staleSkipMax is the most messages in a row a stale stream is read for
// before its cached state is tried again.
const staleSkipMax = 64

func newStateCache(streams int) *stateCache {
	return &stateCache{
		size:    streams,
		order:   list.New(),
		entries: map[string]*list.Element{},
		lanes:   map[laneKey][]any{},
	}
}

// begin returns the state cached for key, if any, and whether to decide from
// it now rather than from the stored state.
func (c *stateCache) begin(key string) (state any, trust bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(e)
	cs := e.Value.(*cachedState)
	if cs.skip > 0 {
		cs.skip--
		return cs.state, false
	}
	return cs.state, true
}

// stale records that the state cached for key was not the stream's, so the
// next messages are decided from the stored state: twice as many each time
// this happens in a row.
func (c *stateCache) stale(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		cs := e.Value.(*cachedState)
		cs.misses++
		cs.skip = min(1<<min(cs.misses, 6), staleSkipMax)
	}
}

// put caches state for key. confirmed says the stream was found where the
// cache had it, so the state can be trusted again at once.
func (c *stateCache) put(key string, state any, confirmed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		cs := e.Value.(*cachedState)
		cs.state = state
		if confirmed {
			cs.misses, cs.skip = 0, 0
		}
		c.order.MoveToFront(e)
		return
	}
	c.entries[key] = c.order.PushFront(&cachedState{key: key, state: state})
	for c.order.Len() > c.size {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.entries, oldest.Value.(*cachedState).key)
	}
}

func (c *stateCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[key]; ok {
		c.order.Remove(e)
		delete(c.entries, key)
	}
}

// enqueue adds item to a lane. It reports whether the lane had no worker, in
// which case the caller must start one.
func (c *stateCache) enqueue(lane laneKey, item any) (start bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting, running := c.lanes[lane]
	c.lanes[lane] = append(waiting, item)
	return !running
}

// take removes up to n of a lane's waiting items, oldest first. When none are
// waiting it closes the lane and returns nil, and the worker must stop.
func (c *stateCache) take(lane laneKey, n int) []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiting := c.lanes[lane]
	if len(waiting) == 0 {
		delete(c.lanes, lane)
		return nil
	}
	n = min(n, len(waiting))
	taken := append([]any(nil), waiting[:n]...)
	c.lanes[lane] = waiting[n:]
	return taken
}

// groupMax is the most messages one transaction records.
const groupMax = 100

// copier returns how to copy a state so that the copy shares nothing with the
// original, or nil when there is no copy that is sure to be faithful, in which
// case the aggregate's streams are not cached: Handled.State must never be
// the cached value itself, which a caller could change.
func (a *Aggregate[S, M, R, Ev, Ef]) copier() func(S) (S, error) {
	switch {
	case a.Clone != nil:
		return func(s S) (S, error) { return a.Clone(s), nil }
	case plainValue(reflect.TypeFor[S]()):
		return func(s S) (S, error) { return s, nil }
	case a.StateVersion != "":
		// Snapshots already rely on the state surviving JSON.
		return func(s S) (S, error) {
			data, err := json.Marshal(s)
			if err != nil {
				return s, err
			}
			var copied S
			err = json.Unmarshal(data, &copied)
			return copied, err
		}
	}
	return nil
}

var plainValues sync.Map // reflect.Type → bool

// plainValue reports whether values of t share nothing when assigned: no
// maps, slices, pointers, interfaces, channels or functions anywhere in them.
// A time.Time counts, although it holds a pointer to its location, since
// locations are never changed.
func plainValue(t reflect.Type) bool {
	if v, ok := plainValues.Load(t); ok {
		return v.(bool)
	}
	plain := true
	switch t.Kind() {
	case reflect.Map, reflect.Slice, reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func, reflect.UnsafePointer:
		plain = false
	case reflect.Array:
		plain = plainValue(t.Elem())
	case reflect.Struct:
		if t == reflect.TypeFor[time.Time]() {
			break
		}
		for i := range t.NumField() {
			if !plainValue(t.Field(i).Type) {
				plain = false
				break
			}
		}
	}
	plainValues.Store(t, plain)
	return plain
}

// cacheKey names a stream's cached state. A snapshot's seq is only meaningful
// to the StateVersion that took it, so each version has its own.
func (a *Aggregate[S, M, R, Ev, Ef]) cacheKey(stream Stream) string {
	return stream.String() + "\x00" + a.StateVersion
}

// handleCached hands r to the stream's lane and waits for its result. A
// caller whose context ends while its message is in flight returns at once,
// and the message may still be recorded, as with any write abandoned
// mid-flight.
func (a *Aggregate[S, M, R, Ev, Ef]) handleCached(ctx context.Context, stream Stream, r *request[S, M, R]) result[S, R] {
	r.done = make(chan result[S, R], 1)
	lane := laneKey{agg: a, name: stream.String()}
	if a.Store.cache.enqueue(lane, r) {
		go a.drain(stream, lane)
	}
	select {
	case res := <-r.done:
		return res
	case <-ctx.Done():
		return result[S, R]{err: ctx.Err()}
	}
}

// drain records a lane's waiting messages until none are left.
func (a *Aggregate[S, M, R, Ev, Ef]) drain(stream Stream, lane laneKey) {
	for {
		taken := a.Store.cache.take(lane, groupMax)
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
// fromCache is set and the cache trusts what it holds, and delivers every
// result.
func (a *Aggregate[S, M, R, Ev, Ef]) record(stream Stream, reqs []*request[S, M, R], fromCache bool) {
	cache, key := a.Store.cache, a.cacheKey(stream)
	ctx, stop := groupContext(reqs)
	defer stop()
	defer func() {
		// A panic in the decider would otherwise end the process from a
		// goroutine no caller can recover in.
		if p := recover(); p != nil {
			cache.drop(key)
			for _, r := range reqs {
				deliver(r, result[S, R]{err: fmt.Errorf("cairn: panic recording a message to %s: %v", stream, p)})
			}
		}
	}()

	var prev, cached *Loaded[S]
	if fromCache {
		if v, trust := cache.begin(key); v != nil {
			if at, ok := v.(Loaded[S]); ok {
				prev = &at
				if trust {
					cached = prev
				}
			}
		}
	}
	results, before, after, err := a.runGroup(ctx, stream, reqs, cached)
	hit := cached != nil && err == nil
	if err != nil && cached != nil && retryable(err) {
		// Stale, or a duplicate key: nothing was recorded. Decide again from
		// the stored state.
		if errors.Is(err, errStale) {
			cache.stale(key)
		}
		results, before, after, err = a.runGroup(ctx, stream, reqs, nil)
	}
	if err != nil {
		cache.drop(key)
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

	// Read from the store, the stream was where the cache had it: trust the
	// cache again.
	confirmed := hit || (cached == nil && prev != nil && before.Seq == prev.Seq)
	cache.put(key, after, confirmed)
	copyState := a.copier()
	for i, r := range reqs {
		res := results[i]
		if errors.Is(res.err, errUnchecked) {
			a.record(stream, []*request[S, M, R]{r}, false)
			continue
		}
		if res.err == nil {
			if s, err := copyState(res.h.State); err == nil {
				res.h.State = s
			} else {
				// Without a copy, the caller gets the state itself, so it
				// must not stay cached.
				cache.drop(key)
			}
		}
		deliver(r, res)
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
