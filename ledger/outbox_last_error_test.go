package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// A failed effect's handler error is written to cairn.outbox.last_error. The
// text comes from outside — the downstream service the handler called — so it
// can carry bytes Postgres text cannot hold (a NUL, invalid UTF-8). The
// dispatcher does not check it, so the UPDATE that records the outcome fails
// with a raw SQLSTATE. The handler's own error is then replaced by the driver
// error, last_error stays NULL, and an operator never sees why the effect
// failed.
func TestAFailedEffectsErrorTextIsNotLostToARawDriverError(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}

	for name, failure := range map[string]error{
		"NUL":          errors.New("downstream rejected it: \x00"),
		"invalid utf8": errors.New("downstream rejected it: \xff"),
	} {
		t.Run(name, func(t *testing.T) {
			// A fresh effect for each case.
			h, err := agg.Handle(ctx, "b", add{1, 9})
			if err != nil {
				t.Fatal(err)
			}
			_ = h
			d := &ledger.Dispatcher[effect]{
				Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects, BatchSize: 1,
				Backoff: func(int) time.Duration { return 0 },
				Handle:  func(context.Context, ledger.Delivery[effect]) error { return failure },
			}
			if _, err := d.DrainOnce(ctx); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) {
					t.Fatalf("DrainOnce returned a raw Postgres error (%s: %s): the handler's error was lost",
						pgErr.SQLState(), pgErr.Message)
				}
				t.Fatalf("DrainOnce: %v", err)
			}
			var lastError *string
			if err := agg.Store.Pool().QueryRow(ctx,
				`SELECT last_error FROM cairn.outbox WHERE stream = 'counter/b'`).Scan(&lastError); err != nil {
				t.Fatal(err)
			}
			if lastError == nil {
				t.Fatalf("last_error is NULL: the handler's error was not recorded")
			}
		})
	}
}
