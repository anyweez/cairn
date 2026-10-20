package ledger_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// The stream name and idempotency key have their own reproductions. Actor and
// correlation ID are also text columns a btree index keys on, and an
// incompressible value longer than the index can hold otherwise reaches
// Postgres and comes back as a raw SQLSTATE 54000. Via and causation ID are
// not indexed, but take the same bound so every piece of entry metadata has
// one rule.
func TestMetadataTooLongForAnIndexIsRefusedCleanly(t *testing.T) {
	t.Parallel()

	buf := make([]byte, 4096)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	long := hex.EncodeToString(buf)

	cases := map[string]ledger.Meta{
		"actor":          {Actor: long},
		"correlation ID": {CorrelationID: long},
		"via":            {Via: long},
		"causation ID":   {CausationID: long},
	}
	for name, meta := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			agg := newCounter(t)
			ctx := ledger.ContextWithMeta(ctx, meta)

			_, err := agg.Handle(ctx, "a", add{1, 100})
			if err == nil {
				t.Fatal("the ledger accepted metadata too long for its own index")
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				t.Fatalf("the caller got a raw Postgres error (%s: %s) instead of the ledger refusing the metadata",
					pgErr.SQLState(), pgErr.Message)
			}
			if entries, readErr := agg.Store.ReadStream(ctx, agg.Stream("a"), 0); readErr != nil {
				t.Fatal(readErr)
			} else if len(entries) != 0 {
				t.Fatalf("the refused message was recorded: %d entries", len(entries))
			}
		})
	}
}

func TestValidateRefusesAnOversizedKind(t *testing.T) {
	t.Parallel()
	agg := counterOn(ledger.New(nil))
	agg.Kind = strings.Repeat("k", 1025)
	if err := agg.Validate(); err == nil || !strings.Contains(err.Error(), "at most 1024 bytes") {
		t.Fatalf("Validate: %v", err)
	}
}
