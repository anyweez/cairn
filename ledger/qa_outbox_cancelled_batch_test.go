package ledger_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A dispatcher stopped while it is carrying out a batch — a deploy cancels the
// context DrainOnce runs under — leaves the effects it has not reached yet in
// the outbox, which is the point of at-least-once delivery. But the claim
// counted an attempt against every effect in the batch, and MaxAttempts is
// decided on that count, so the effects the dispatcher never reached are given
// up on by the next drain without Handle ever being called for them: work that
// was asked for is discarded, and last_error says it was attempted.
//
// The contract MaxAttempts states is "gives up on an effect after this many
// attempts", so an effect marked failed must have been handed to the handler.
func TestAnOutboxEffectIsNotGivenUpOnBeforeItIsHandedOut(t *testing.T) {
	store := ledgertest.NewStore(t, ledger.RequireActor(false))
	agg := counterOn(store)
	const effects = 3
	for i := range effects {
		if _, err := agg.Handle(ctx, fmt.Sprintf("s%d", i), add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu        sync.Mutex
		delivered = map[string]int{}
		started   = make(chan struct{}, 1)
		stopped   = make(chan struct{})
	)
	// The operator's configuration, which every restart of the dispatcher keeps.
	// One attempt: a handler that fails is not to be retried, only stopped.
	const maxAttempts = 1
	dispatcher := func(handle func(context.Context, ledger.Delivery[effect]) error) *ledger.Dispatcher[effect] {
		return &ledger.Dispatcher[effect]{
			Store: store, Kind: "counter", Effects: agg.Codecs.Effects,
			MaxAttempts: maxAttempts,
			// Short, so the rows the stop left behind are claimable again by
			// the time the restarted dispatcher drains.
			Lease:     50 * time.Millisecond,
			BatchSize: effects,
			Handle:    handle,
		}
	}
	d := dispatcher(func(hctx context.Context, dl ledger.Delivery[effect]) error {
		mu.Lock()
		delivered[dl.Key()]++
		mu.Unlock()
		started <- struct{}{}
		// Shutdown begins while this effect is in flight.
		<-stopped
		return hctx.Err()
	})

	// Stop the dispatcher while the first effect of the claimed batch is in
	// flight, as a deploy does.
	cancelCtx, cancel := context.WithCancel(ctx)
	go func() {
		<-started
		cancel()
		close(stopped)
	}()
	if _, err := d.DrainOnce(cancelCtx); err == nil {
		t.Fatalf("DrainOnce after the stop returned no error")
	}

	// The dispatcher is restarted with the same configuration, into a handler
	// that would carry every effect out. Anything already marked failed never
	// gets here.
	time.Sleep(80 * time.Millisecond)
	ok := dispatcher(func(_ context.Context, dl ledger.Delivery[effect]) error {
		mu.Lock()
		delivered[dl.Key()]++
		mu.Unlock()
		return nil
	})
	for {
		n, err := ok.DrainOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}

	rows, err := store.Pool().Query(ctx,
		`SELECT stream, seq, idx, attempts, done_at IS NOT NULL, failed_at IS NOT NULL, coalesce(last_error, '')
		 FROM cairn.outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			stream, last  string
			seq           int64
			idx, attempts int
			done, failed  bool
		)
		if err := rows.Scan(&stream, &seq, &idx, &attempts, &done, &failed, &last); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("%s#%d.%d", stream, seq, idx)
		mu.Lock()
		handled := delivered[key]
		mu.Unlock()
		if failed && handled == 0 {
			t.Errorf("effect %s was marked failed after %d attempts (%q) but its handler was never called: "+
				"it was claimed as part of a batch the dispatcher was stopped in the middle of",
				key, attempts, last)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
