package ledger_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// An idempotency key is stored in `cairn.entries.idempotency_key`, which the
// unique index `entries_idempotency` keys on. Postgres cannot build a btree
// index entry larger than about 2704 bytes, so a merely long key — valid
// UTF-8, no NUL — reaches the server and the insert fails with a raw SQLSTATE
// 54000 instead of an error the ledger produced. Nothing is recorded, so no
// data is corrupted; the caller just cannot tell a bad request from an
// infrastructure failure. The key is random so it cannot be compressed away.
func TestAnIdempotencyKeyTooLongForTheIndexIsRefusedCleanly(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)

	buf := make([]byte, 4096)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	key := hex.EncodeToString(buf) // 8192 incompressible characters

	h, err := agg.Handle(ctx, "a", add{1, 100}, ledger.IdempotencyKey(key))
	if err == nil {
		t.Fatalf("a key of %d bytes was accepted: %+v", len(key), h)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the caller got a raw Postgres error (%s: %s) instead of the ledger refusing the key",
			pgErr.SQLState(), pgErr.Message)
	}
}
