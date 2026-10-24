package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// An effect's row is removed once its delivery is recorded: the row exists to
// hand the effect out, and a delivered effect has nothing left to read. If the
// row stayed, every delivered effect would sit in the outbox for ever.
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
			// Each effect is claimed once, so the attempt a handler sees is
			// its first; a row reclaimed while in flight would be seen twice.
			if d.Attempt != 1 {
				t.Errorf("%s handed out on attempt %d, want 1", d.Key(), d.Attempt)
			}
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
	var left int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(*) FROM cairn.outbox`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d delivered effects left rows in the outbox; want none", left)
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
	var attempts []int
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
		Backoff: func(int) time.Duration { return 0 },
		Handle: func(_ context.Context, d ledger.Delivery[effect]) error {
			calls++
			attempts = append(attempts, d.Attempt)
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
	if calls != 2 || fmt.Sprint(attempts) != "[1 2]" {
		t.Fatalf("the handler ran %d times on attempts %v, want 2 on [1 2]: a panic must be retried", calls, attempts)
	}
	var left int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(*) FROM cairn.outbox`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d rows left in the outbox after delivery; want none", left)
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
