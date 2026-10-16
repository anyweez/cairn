package decisions

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A stream ID that Postgres cannot store must be refused by the ledger, the
// way an empty one is, rather than handed to the driver and failing with a
// SQLSTATE. The register takes the ID from its caller (Get, AddOption, ...),
// so a NUL byte or invalid UTF-8 in a path parameter reaches it.
func TestStreamIDThatPostgresCannotStoreIsRefusedCleanly(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))

	ids := map[string]string{
		"NUL":          "d-\x00x",
		"invalid utf8": "d-\xffx",
	}

	for name, id := range ids {
		t.Run("read/"+name, func(t *testing.T) {
			_, err := svc.Get(ctx, id)
			assertNotADriverError(t, err)
		})
		t.Run("write/"+name, func(t *testing.T) {
			_, err := svc.AddOption(ctx, id, "ana", "an option")
			assertNotADriverError(t, err)
		})
	}
}

func assertNotADriverError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error for a stream ID Postgres cannot store")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the caller got a raw Postgres error (%s: %s) instead of the ledger refusing the stream ID",
			pgErr.SQLState(), pgErr.Message)
	}
}
