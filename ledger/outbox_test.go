package ledger_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// Two dispatchers over one outbox must claim disjoint effects. Effects are
// claimed with a lease, so the second dispatcher's SKIP LOCKED sees nothing of
// what the first claimed and each effect is delivered once.
func TestTwoDispatchersDeliverEachEffectOnce(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	const effects = 20
	for range effects {
		if _, err := agg.Handle(ctx, "a", add{1, 1000}); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	delivered := map[string]int{}
	counts := make([]int, 2)
	// Neither dispatcher may finish the effect it first claims until both have
	// claimed one, so the two are known to have overlapped rather than one
	// draining the outbox before the other ran.
	var claimed sync.WaitGroup
	claimed.Add(len(counts))
	release := make(chan struct{})
	handle := func(id int) func(context.Context, ledger.Delivery[effect]) error {
		var first sync.Once
		return func(_ context.Context, d ledger.Delivery[effect]) error {
			mu.Lock()
			delivered[d.Key()]++
			counts[id]++
			mu.Unlock()
			first.Do(func() {
				claimed.Done()
				<-release
			})
			return nil
		}
	}
	dispatcher := func(id int) *ledger.Dispatcher[effect] {
		return &ledger.Dispatcher[effect]{
			Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
			BatchSize: 5, Lease: time.Minute, Handle: handle(id),
		}
	}

	var wg sync.WaitGroup
	for id := range counts {
		d := dispatcher(id)
		wg.Go(func() {
			for {
				n, err := d.DrainOnce(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		})
	}
	// Release the two once both have claimed; the timeout keeps a claim that
	// failed outright from hanging the test.
	both := make(chan struct{})
	go func() { claimed.Wait(); close(both) }()
	select {
	case <-both:
	case <-time.After(10 * time.Second):
	}
	close(release)
	wg.Wait()

	if len(delivered) != effects {
		t.Fatalf("delivered %d of %d effects", len(delivered), effects)
	}
	for id, n := range counts {
		if n == 0 {
			t.Fatalf("dispatcher %d delivered nothing; the two did not overlap", id)
		}
	}
	for key, n := range delivered {
		if n != 1 {
			t.Errorf("%s was delivered %d times", key, n)
		}
	}
	var pending, attempts int
	if err := agg.Store.Pool().QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE done_at IS NULL), coalesce(max(attempts), 0) FROM cairn.outbox`).
		Scan(&pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || attempts != 1 {
		t.Fatalf("%d effects unfinished with a top attempt count of %d; want 0 and 1", pending, attempts)
	}
}

// A handler that panics fails its delivery like any other error: the panic is
// caught, the effect is put back, and the next claim retries it.
func TestAPanickingHandlerIsRetried(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}

	var calls int
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
		Backoff: func(int) time.Duration { return 0 },
		Handle: func(context.Context, ledger.Delivery[effect]) error {
			calls++
			if calls == 1 {
				panic("downstream exploded")
			}
			return nil
		},
	}
	for {
		n, err := d.DrainOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if calls != 2 {
		t.Fatalf("the handler ran %d times, want 2: a panic must be retried", calls)
	}
	var done bool
	var lastError *string
	var attempts int
	if err := agg.Store.Pool().QueryRow(ctx,
		`SELECT done_at IS NOT NULL, last_error, attempts FROM cairn.outbox`).
		Scan(&done, &lastError, &attempts); err != nil {
		t.Fatal(err)
	}
	if !done || attempts != 2 || lastError != nil {
		t.Fatalf("done %v, attempts %d, last error %v; want delivery to finish on attempt 2", done, attempts, lastError)
	}
}

// An error draining the outbox ends Run unless OnError is set, and with OnError
// Run reports it and keeps going. A closed pool is an error the context did not
// cause, which is the kind Run must decide about.
func TestRunReportsAndSurvivesErrors(t *testing.T) {
	t.Parallel()

	t.Run("OnError keeps Run going", func(t *testing.T) {
		t.Parallel()
		agg := newCounter(t)
		agg.Store.Pool().Close()

		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var mu sync.Mutex
		var seen []error
		d := &ledger.Dispatcher[effect]{
			Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
			OnError: func(err error) { mu.Lock(); seen = append(seen, err); mu.Unlock(); cancel() },
			Handle:  func(context.Context, ledger.Delivery[effect]) error { return nil },
		}
		if err := d.Run(runCtx, time.Millisecond); !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v; want the context's error once OnError stopped it", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(seen) == 0 {
			t.Fatal("OnError was never called")
		}
	})

	t.Run("without OnError Run returns the error", func(t *testing.T) {
		t.Parallel()
		agg := newCounter(t)
		agg.Store.Pool().Close()

		d := &ledger.Dispatcher[effect]{
			Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
			Handle: func(context.Context, ledger.Delivery[effect]) error { return nil },
		}
		if err := d.Run(ctx, time.Millisecond); err == nil {
			t.Fatal("Run returned nil while every claim failed")
		}
	})
}
