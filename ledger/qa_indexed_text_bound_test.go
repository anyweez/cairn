package ledger_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// indexedText returns n bytes of hex that pg's btree cannot compress away: a
// repeated byte string is deduplicated by TOAST compression and fits an index
// whatever its length, so the value has to look like real text.
func indexedText(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)[:n]
}

// requiresACairnError fails when err is (or wraps) a database error: cairn is
// supposed to refuse text it cannot index itself, the way it does for a stream
// name, an idempotency key and an aggregate kind, all of which are bounded to
// maxIndexedTextBytes before any statement is sent.
func requiresACairnError(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s was accepted", what)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("%s reached the database and came back as SQLSTATE %s: %v", what, pgErr.Code, pgErr)
	}
	if strings.Contains(err.Error(), "btree") || strings.Contains(err.Error(), "index row size") {
		t.Fatalf("%s failed on an index instead of being refused: %v", what, err)
	}
}

// A store keeps a snapshot per stream per StateVersion, and state_snapshots is
// keyed by (stream, version): that primary key is a btree, and a btree entry
// cannot exceed about 2704 bytes. Aggregate.Validate bounds the Kind and the
// stream name for exactly this reason and says so ("bounds every text value
// cairn puts in a btree index"), but StateVersion is unbounded. A version past
// the limit makes every snapshot write fail, and a snapshot is due on every
// message from then on: the stream records nothing more, ever, and the caller
// is told about an index rather than about the state version.
func TestQASnapshotStateVersionIsBounded(t *testing.T) {
	store := ledgertest.NewStore(t, ledger.RequireActor(false), ledger.StateCacheSize(0))
	agg := counterOn(store)
	agg.StateVersion = indexedText(t, 3000)
	agg.SnapshotEvery = 3

	var (
		version  error
		recorded int
	)
	for i := range 4 {
		if _, err := agg.Handle(context.Background(), "a", add{1, 100}); err != nil {
			if version != nil && !strings.Contains(err.Error(), "snapshot") {
				t.Fatalf("message %d failed with something else: %v", i+1, err)
			}
			version = err
			continue
		}
		recorded++
	}
	if version == nil {
		t.Fatalf("a %d-byte state version was accepted and the stream recorded %d messages", len(agg.StateVersion), recorded)
	}
	requiresACairnError(t, "a state version Postgres cannot index", version)
	if !strings.Contains(strings.ToLower(version.Error()), "version") {
		t.Fatalf("the error does not name the state version that is too long: %v", version)
	}
}

// The same for a projection's checkpoint: cairn.checkpoints is keyed by name,
// which Projection.Validate requires to be non-empty and nothing more, so a
// name past the btree limit makes every CatchUp fail, and the projection never
// applies an entry. cairn bounds this text elsewhere for the same reason.
func TestQAProjectionNameIsBounded(t *testing.T) {
	store := ledgertest.NewStore(t, ledger.RequireActor(false), ledger.StateCacheSize(0))
	agg := counterOn(store)
	for range 2 {
		if _, err := agg.Handle(context.Background(), "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}
	p := &ledger.Projection{
		Store: store, Name: indexedText(t, 3000),
		Apply: func(context.Context, *ledger.Batch, ledger.Entry) error { return nil },
	}
	if _, err := p.CatchUp(context.Background()); err != nil {
		requiresACairnError(t, "a projection name Postgres cannot index", err)
		if !strings.Contains(strings.ToLower(err.Error()), "name") {
			t.Fatalf("the error does not name the projection that is too long: %v", err)
		}
		return
	}
	t.Fatalf("a %d-byte projection name was accepted", len(p.Name))
}
