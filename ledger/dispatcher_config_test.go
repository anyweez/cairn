package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// panicked runs f and reports whether it panicked instead of returning.
func panicked(f func() error) (err error, panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			panicked = true
		}
	}()
	return f(), false
}

// A Dispatcher is configured like an Aggregate or a Projection, by a struct
// literal, and the ledger's other two components check theirs before doing
// anything: "a mistake is an error rather than a nil-pointer panic", as
// Aggregate.Validate puts it. A Dispatcher does not, so a dispatcher wired up
// without a Store dereferences nil out of Run and DrainOnce — the two methods
// a process starts at boot.
func TestADispatcherWithNoStoreReportsItself(t *testing.T) {
	var d ledger.Dispatcher[effect]

	if err, p := panicked(func() error { _, err := d.DrainOnce(context.Background()); return err }); p {
		t.Fatal("DrainOnce panicked on a dispatcher with no Store; want an error naming the missing field")
	} else if err == nil {
		t.Fatal("DrainOnce returned nil for a dispatcher with no Store")
	}

	if err, p := panicked(func() error { return d.Run(context.Background(), time.Millisecond) }); p {
		t.Fatal("Run panicked on a dispatcher with no Store; want an error naming the missing field")
	} else if err == nil {
		t.Fatal("Run returned nil for a dispatcher with no Store")
	}
}

// A dispatcher with no Handle is worse than a panic: deliver recovers the nil
// call, records it as a failed attempt and moves on, so with the default
// MaxAttempts every effect is retried forever against a handler that cannot
// run, and nothing is ever delivered. The effect must not be claimed at all.
func TestADispatcherWithNoHandleClaimsNothing(t *testing.T) {
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
	}
	if err, p := panicked(func() error { _, err := d.DrainOnce(context.Background()); return err }); p {
		t.Fatal("DrainOnce panicked on a dispatcher with no Handle; want an error naming the missing field")
	} else if err == nil {
		t.Fatal("DrainOnce returned nil for a dispatcher with no Handle; the effect was claimed and retried against a handler that cannot run")
	}

	var attempts int
	var lastError *string
	if err := agg.Store.Pool().QueryRow(ctx,
		`SELECT attempts, last_error FROM cairn.outbox WHERE stream = 'counter/a'`).Scan(&attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 || lastError != nil {
		t.Fatalf("the effect was claimed %d times (last_error %v) by a dispatcher that cannot carry it out",
			attempts, *lastError)
	}
}
