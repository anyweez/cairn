package ledger_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/ledger"
)

// insertCounterEntry writes one counter entry for id straight into the ledger,
// as a release before the index-length bound could have. The read paths are
// then tested against data the current write path would refuse to create.
func insertCounterEntry(t *testing.T, store *ledger.Store, id, key string) {
	t.Helper()
	stream := ledger.Stream{Kind: "counter", ID: id}
	if _, err := store.Pool().Exec(ctx,
		`INSERT INTO cairn.streams (stream, kind, seq) VALUES ($1, $2, 1)`,
		stream.String(), stream.Kind); err != nil {
		t.Fatalf("inserting stream: %v", err)
	}
	var keyArg any
	if key != "" {
		keyArg = key
	}
	if _, err := store.Pool().Exec(ctx, `
INSERT INTO cairn.entries (stream, kind, seq, message, result, events, idempotency_key)
VALUES ($1, $2, 1, $3, $4, $5, $6)`,
		stream.String(), stream.Kind,
		`{"type":"add","v":1,"data":{"N":5,"Cap":100}}`,
		`{"type":"total","v":1,"data":5}`,
		`[{"type":"added","v":1,"data":{"N":5}}]`,
		keyArg); err != nil {
		t.Fatalf("inserting entry: %v", err)
	}
}

// #98 bounded a stream name's length in the validator the read paths share, so
// a name written before that bound — between 1024 bytes and Postgres's btree
// limit — could no longer be loaded. The bound is about writing a new index
// entry, not reading one Postgres already stored.
func TestAStreamNameStoredBeforeTheLengthBoundStillLoads(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	id := strings.Repeat("x", 1500)
	insertCounterEntry(t, agg.Store, id, "")

	loaded, err := agg.Load(ctx, id)
	if err != nil {
		t.Fatalf("loading a stream Postgres already stored: %v", err)
	}
	if loaded.State.Total != 5 || loaded.Seq != 1 {
		t.Fatalf("loaded %+v", loaded)
	}

	stream := ledger.Stream{Kind: "counter", ID: id}
	entries, err := agg.Store.ReadStream(ctx, stream, 0)
	if err != nil {
		t.Fatalf("reading a stream Postgres already stored: %v", err)
	}
	if len(entries) != 1 || entries[0].Seq != 1 {
		t.Fatalf("entries: %+v", entries)
	}

	// Appending to the existing stream is also allowed: the bound is about
	// creating one. The second handle is served from the state cache.
	for i, want := range []int{6, 7} {
		h, err := agg.Handle(ctx, id, add{1, 100})
		if err != nil {
			t.Fatalf("handle %d: %v", i, err)
		}
		if h.Entry.Seq != int64(i+2) || h.Result != want {
			t.Fatalf("handle %d recorded seq %d with result %d; want %d and %d", i, h.Entry.Seq, h.Result, i+2, want)
		}
	}

	if _, err := agg.Verify(ctx, id); err != nil {
		t.Fatalf("verifying a stream Postgres already stored: %v", err)
	}

	// Creating a stream with such a name is still refused.
	_, err = agg.Handle(ctx, strings.Repeat("y", 1500), add{1, 100})
	if err == nil || !strings.Contains(err.Error(), "longer than the 1024-byte limit") {
		t.Fatalf("a new stream name past the bound: %v", err)
	}
}

// The same bound was applied when an idempotency key was looked up, so a retry
// whose first attempt was recorded before #98 was refused instead of answered.
func TestAnIdempotencyKeyStoredBeforeTheLengthBoundStillAnswersARetry(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	key := strings.Repeat("k", 1500)
	insertCounterEntry(t, agg.Store, "a", key)

	// The second handle is served from the state cache, which finds a duplicate
	// on the unique index and looks the key up without the cache.
	for i := range 2 {
		h, err := agg.Handle(ctx, "a", add{5, 100}, ledger.IdempotencyKey(key))
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if !h.Duplicate || h.Result != 5 {
			t.Fatalf("retry %d: %+v", i, h)
		}
	}
}
