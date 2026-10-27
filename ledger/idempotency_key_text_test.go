package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// An idempotency key goes to Postgres as a text parameter twice: once when the
// key is looked up, and once when the entry carrying it is written. A key with
// a NUL or text that is not valid UTF-8 therefore failed with a raw driver
// error instead of an error cairn produced, so an application could not tell a
// malformed key (its caller's fault, a 400) from an infrastructure failure (a
// 500). Handle possibly looks the key up, and possibly only writes it once the
// stream is cached, so both paths are checked, on both entry points.
func TestAnIdempotencyKeyTheLedgerCannotStoreIsRefused(t *testing.T) {
	t.Parallel()

	type counterAgg = ledger.Aggregate[counter, msg, int, event, effect]
	record := map[string]func(*counterAgg, string) error{
		"Handle": func(agg *counterAgg, key string) error {
			_, err := agg.Handle(ctx, "a", add{1, 100}, ledger.IdempotencyKey(key))
			return err
		},
		"Apply": func(agg *counterAgg, key string) error {
			return agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("a")}, func(tx *ledger.Tx) error {
				at, err := agg.LoadLocked(ctx, tx, "a")
				if err != nil {
					return err
				}
				_, err = agg.Apply(ctx, tx, &at, add{1, 100}, ledger.IdempotencyKey(key))
				return err
			})
		},
	}
	caches := map[string]ledger.StoreOption{
		"cached":   ledger.StateCacheSize(1),
		"uncached": ledger.StateCacheSize(0),
	}

	for cacheName, cache := range caches {
		for pathName, record := range record {
			t.Run(cacheName+"/"+pathName, func(t *testing.T) {
				t.Parallel()
				agg := counterOn(ledgertest.NewStore(t, cache))
				// Warm the stream so Handle takes the cached path, where the
				// key is written but never looked up.
				if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
					t.Fatal(err)
				}
				for name, key := range map[string]string{
					"NUL":          "req\x00-1",
					"invalid utf8": "req\xff-1",
				} {
					err := record(agg, key)
					if err == nil {
						t.Fatalf("%s: expected an error for a key Postgres cannot store", name)
					}
					var pgErr *pgconn.PgError
					if errors.As(err, &pgErr) {
						t.Fatalf("%s: caller got a raw Postgres error (%s: %s) instead of a cairn error",
							name, pgErr.SQLState(), pgErr.Message)
					}
					if !strings.HasPrefix(err.Error(), "cairn: ") {
						t.Fatalf("%s: the refusal should be a cairn error, got %T: %v", name, err, err)
					}
				}
				entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 {
					t.Fatalf("stream a has %d entries, want the 1 recorded before it: nothing with an unstorable key may be recorded",
						len(entries))
				}
			})
		}
	}
}
