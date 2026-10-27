package ledger_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn/ledger"
)

// The stream name and idempotency key have their own reproductions. Actor and
// correlation ID are also text columns a btree index keys on, and an
// incompressible value longer than the index can hold otherwise reaches
// Postgres and comes back as a raw SQLSTATE 54000. Via and causation ID are
// not indexed, so they have no index entry whose size their length could
// exceed, and are not bounded.
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

// A kind longer than the index bound is still valid to read: a stream written
// before the bound existed may have one. The bound applies where a new stream
// is created, which is the only place the name reaches an index.
func TestAnOversizedKindIsRefusedWhenANewStreamIsCreated(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	agg.Kind = strings.Repeat("k", 1025)
	if err := agg.Validate(); err != nil {
		t.Fatalf("Validate refused a kind a ledger may already hold: %v", err)
	}
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err == nil ||
		!strings.Contains(err.Error(), "1027 bytes, longer than the 1024-byte limit") {
		t.Fatalf("Handle: %v", err)
	}
}

// Via and causation ID are not indexed, so a long value is stored and read
// back unchanged rather than refused for an index's sake.
func TestUnindexedMetadataIsNotBounded(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	long := strings.Repeat("m", 8192)
	meta := ledger.Meta{Via: long, CausationID: long}

	if _, err := agg.Handle(ledger.ContextWithMeta(ctx, meta), "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Meta.Via != long || entries[0].Meta.CausationID != long {
		t.Fatalf("stored metadata: %+v", entries)
	}
}

// A refusal that describes an over-long value must not repeat it: the caller
// already has the 8KB it sent, so echoing it back makes every refused request
// cost 8KB in a response or a log line. The error names the length and the
// limit, and quotes only the start of the value.
func TestAnOverLongValueIsNotEchoedInTheRefusal(t *testing.T) {
	t.Parallel()

	type counterAgg = ledger.Aggregate[counter, msg, int, event, effect]

	buf := make([]byte, 4096)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	long := hex.EncodeToString(buf) // 8192 incompressible characters

	refuse := map[string]struct {
		size int // the refused value's length, which the error must report
		do   func(*counterAgg) error
	}{
		"stream ID": {len("counter/") + len(long), func(agg *counterAgg) error {
			_, err := agg.Handle(ctx, long, add{1, 100})
			return err
		}},
		"kind": {len(long) + len("/a"), func(agg *counterAgg) error {
			agg.Kind = long
			_, err := agg.Handle(ctx, "a", add{1, 100})
			return err
		}},
		"idempotency key": {len(long), func(agg *counterAgg) error {
			_, err := agg.Handle(ctx, "a", add{1, 100}, ledger.IdempotencyKey(long))
			return err
		}},
		"actor": {len(long), func(agg *counterAgg) error {
			_, err := agg.Handle(ledger.ContextWithMeta(ctx, ledger.Meta{Actor: long}), "a", add{1, 100})
			return err
		}},
		"correlation ID": {len(long), func(agg *counterAgg) error {
			_, err := agg.Handle(ledger.ContextWithMeta(ctx, ledger.Meta{CorrelationID: long}), "a", add{1, 100})
			return err
		}},
	}
	for name, c := range refuse {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := c.do(newCounter(t))
			if err == nil {
				t.Fatal("the ledger accepted a value too long for its own index")
			}
			msg := err.Error()
			if len(msg) > 400 {
				t.Fatalf("the refusal is %d bytes, so it echoes the value back: %.120q", len(msg), msg)
			}
			if strings.Contains(msg, long) {
				t.Fatal("the refusal contains the whole value")
			}
			// A prefix of the value is quoted so the caller can tell which one
			// was refused; the rest is left out. long[:48] is inside the prefix
			// even when the value is the stream name, which starts "counter/".
			if want := long[:48]; !strings.Contains(msg, want) {
				t.Fatalf("the refusal does not quote the start of the value: %q", msg)
			}
			if strings.Contains(msg, long[64:]) {
				t.Fatal("the refusal quotes the value past its first 64 bytes")
			}
			if want := fmt.Sprintf("%d bytes, longer than the 1024-byte limit", c.size); !strings.Contains(msg, want) {
				t.Fatalf("the refusal does not say %q: %q", want, msg)
			}
		})
	}
}
