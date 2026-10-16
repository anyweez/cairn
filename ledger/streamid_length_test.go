package ledger_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A stream ID the caller sends is stored in cairn.streams.stream, which has a
// btree primary key, and in cairn.entries, whose unique index and foreign key
// also key on it. Postgres refuses an index key larger than its btree limit,
// so an ID that is merely long — valid UTF-8, no NUL — reaches the server and
// comes back as a raw SQLSTATE 54000 instead of an error the ledger produces.
// An application cannot tell a malformed ID (a 400) from an infrastructure
// failure (a 500).
func TestAStreamIDTooLongForTheIndexIsRefusedCleanly(t *testing.T) {
	agg := newCounter(t)

	// Random hex, so the value does not compress and the btree limit is the
	// one that binds.
	buf := make([]byte, 4096)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	id := hex.EncodeToString(buf)

	_, err := agg.Handle(ctx, id, add{1, 100})
	if err == nil {
		t.Fatal("the ledger accepted a stream ID too long for its own index")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the caller got a raw Postgres error (%s: %s) instead of the ledger refusing the stream ID",
			pgErr.SQLState(), pgErr.Message)
	}
}
