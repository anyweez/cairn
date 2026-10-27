package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// Dispatcher.Validate is the dispatcher's startup check, the counterpart of
// Aggregate.Validate and Projection.Validate. It reports a missing Store, an
// empty Kind, a missing Effects codec and a missing Handle, but not a Kind the
// ledger cannot use. DrainOnce passes Kind to the claim statement as a text
// parameter, so a NUL or invalid UTF-8 comes back from the driver as SQLSTATE
// 22021 instead of an error cairn produced: a setup mistake is indistinguishable
// from an infrastructure failure. A slash is worse: no aggregate can have such a
// Kind, so the claim matches nothing and DrainOnce reports success for ever
// while the outbox grows and every effect stays undelivered.
func TestADispatcherWithAnUnstorableKindIsRefused(t *testing.T) {
	agg := newCounter(t)
	if _, err := agg.Handle(context.Background(), "a", add{1, 100}); err != nil {
		t.Fatal(err)
	}

	for name, kind := range map[string]string{
		"NUL":          "counter\x00x",
		"invalid utf8": "counter\xffx",
		"slash":        "counter/x",
	} {
		t.Run(name, func(t *testing.T) {
			var handled int
			d := &ledger.Dispatcher[effect]{
				Store: agg.Store, Kind: kind, Effects: agg.Codecs.Effects,
				Handle: func(context.Context, ledger.Delivery[effect]) error {
					handled++
					return nil
				},
			}

			if err := d.Validate(); err == nil {
				t.Fatalf("Validate accepted a dispatcher whose Kind the ledger cannot use")
			}
			n, err := d.DrainOnce(context.Background())
			if err == nil {
				t.Fatalf("DrainOnce with an unusable Kind claimed %d effects and returned no error", n)
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				t.Fatalf("DrainOnce returned a raw Postgres error (SQLSTATE %s: %v) instead of refusing the Kind", pgErr.Code, err)
			}
			if handled != 0 {
				t.Fatalf("the handler was called %d times by a dispatcher that could not run", handled)
			}
		})
	}
}
