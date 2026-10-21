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

// A dispatcher's claim lease covers the whole batch at once, but the lease is
// only restarted for an effect when the walk reaches it. Effects later in the
// batch are therefore claimable by a second dispatcher while the first is
// still carrying out an earlier one. The first dispatcher's claim of those
// effects is not a delivery, yet it raises their attempt count, so a second
// dispatcher with MaxAttempts=1 marks them failed having never handed one to a
// handler: work that was asked for is discarded, and last_error says it was
// attempted.
//
// The contract MaxAttempts states is "gives up on an effect after this many
// attempts", and t2818's fix established that an effect marked failed must
// have been handed to the handler.
func TestQAEffectsLaterInABatchAreFailedWithoutDeliveryWhenItsLeaseExpires(t *testing.T) {
	store := ledgertest.NewStore(t, ledger.RequireActor(false))
	agg := counterOn(store)
	const effects = 8
	for range effects {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}

	var (
		mu        sync.Mutex
		delivered = map[string]int{}
	)
	count := func(d ledger.Delivery[effect]) {
		mu.Lock()
		delivered[d.Key()]++
		mu.Unlock()
	}
	const (
		lease = 2 * time.Second
		// Each handler spends well under its lease, so nothing here outlives
		// the context the dispatcher gave it.
		work      = 600 * time.Millisecond
		maxTries  = 1
		batchSize = effects
	)

	started := make(chan struct{}, 1)
	a := &ledger.Dispatcher[effect]{
		Store: store, Kind: "counter", Effects: agg.Codecs.Effects,
		BatchSize: batchSize, Lease: lease, MaxAttempts: maxTries,
		Handle: func(_ context.Context, d ledger.Delivery[effect]) error {
			count(d)
			select {
			case started <- struct{}{}:
			default:
			}
			time.Sleep(work)
			return nil
		},
	}
	doneA := make(chan error, 1)
	go func() {
		_, err := a.DrainOnce(ctx)
		doneA <- err
	}()
	<-started
	// The batch's claim lease expires while the first dispatcher is still
	// working through the batch, but before it reaches the effects at the end.
	time.Sleep(lease + 150*time.Millisecond)

	b := &ledger.Dispatcher[effect]{
		Store: store, Kind: "counter", Effects: agg.Codecs.Effects,
		BatchSize: batchSize, Lease: time.Minute, MaxAttempts: maxTries,
		Handle: func(_ context.Context, d ledger.Delivery[effect]) error {
			count(d)
			return nil
		},
	}
	if _, err := b.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-doneA; err != nil {
		t.Fatal(err)
	}

	rows, err := store.Pool().Query(ctx,
		`SELECT stream, seq, idx, attempts, failed_at IS NOT NULL FROM cairn.outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	undelivered := 0
	for rows.Next() {
		var (
			stream    string
			seq       int64
			idx, atts int
			failed    bool
		)
		if err := rows.Scan(&stream, &seq, &idx, &atts, &failed); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("%s#%d.%d", stream, seq, idx)
		mu.Lock()
		handled := delivered[key]
		mu.Unlock()
		if failed && handled == 0 {
			undelivered++
			t.Logf("%s was marked failed after %d attempts but its handler was never called", key, atts)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if undelivered > 0 {
		t.Fatalf("%d effect(s) marked failed without ever being handed to a handler", undelivered)
	}
}
