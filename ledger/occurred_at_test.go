package ledger_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// Meta.OccurredAt is a producer-supplied time, passed straight to the
// occurred_at timestamptz parameter. queueAppend checks the metadata text and
// the zero value, but not the range timestamptz can hold. A time outside
// Postgres's range is rejected by the server with a raw SQLSTATE instead of an
// error cairn produced.
func TestOccurredAtOutsideThePostgresRangeIsNotARawDriverError(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)

	for name, at := range map[string]time.Time{
		"year 300000": time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC),
		"year -50000": time.Date(-50000, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := agg.Handle(ctx, "a", add{1, 10}, ledger.WithMeta(ledger.Meta{OccurredAt: at}))
			if err == nil {
				t.Fatalf("expected an error for a time outside the timestamptz range")
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				t.Fatalf("caller got a raw Postgres error (%s: %s) instead of a cairn error",
					pgErr.SQLState(), pgErr.Message)
			}
		})
	}
}
