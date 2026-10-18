package ledger_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// A stream ID or kind that Postgres cannot store is rejected cleanly on the
// write paths (validStream, used by locking) and by Load. The read paths call
// the same SQL with the same caller-supplied name but skip validStream, so the
// caller gets a raw *pgconn.PgError instead of an error cairn produced. An
// application cannot tell a malformed ID (its caller's fault, a 400) from an
// infrastructure failure (a 500).
//
// Same root as t2555's stream-ID case, on the paths its fix did not reach.
func TestReadPathsRejectAStreamNamePostgresCannotStore(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}

	assertNotDriverError := func(t *testing.T, what string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: expected an error for a name Postgres cannot store", what)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatalf("%s: caller got a raw Postgres error (%s: %s) instead of a cairn error",
				what, pgErr.SQLState(), pgErr.Message)
		}
	}

	for name, id := range map[string]string{
		"NUL":          "a\x00b",
		"invalid utf8": "a\xffb",
	} {
		t.Run("ReadStream/"+name, func(t *testing.T) {
			_, err := agg.Store.ReadStream(ctx, ledger.Stream{Kind: "counter", ID: id}, 0)
			assertNotDriverError(t, "ReadStream", err)
		})
		t.Run("Verify/"+name, func(t *testing.T) {
			_, err := agg.Verify(ctx, id)
			assertNotDriverError(t, "Verify", err)
		})
	}

	for name, kind := range map[string]string{
		"NUL":          "counter\x00x",
		"invalid utf8": "counter\xffx",
	} {
		t.Run("Streams/"+name, func(t *testing.T) {
			_, err := agg.Store.Streams(ctx, kind)
			assertNotDriverError(t, "Streams", err)
		})
		t.Run("ReadAll/"+name, func(t *testing.T) {
			_, _, err := agg.Store.ReadAll(ctx, ledger.Cursor{}, 10, kind)
			assertNotDriverError(t, "ReadAll", err)
		})
	}
}
