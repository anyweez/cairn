package ledger_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A message that carries a JSON number, the way a payload passed through from a
// caller does. jsonb stores a number as Postgres numeric, which cannot hold a
// magnitude beyond 131072 digits before the decimal point or 16383 after it,
// so a number json.Marshal accepts can still be refused by the column the entry
// lands in.
type zzqaRawNumberPayload struct {
	Payload json.RawMessage `json:"payload"`
}

func (zzqaRawNumberPayload) isZZQANumberMsg() {}

// json.Number is the standard library's own passthrough for a number literal:
// the codec sees a string and json.Marshal writes the literal unchanged.
type zzqaNumberLiteral struct {
	N json.Number `json:"n"`
}

func (zzqaNumberLiteral) isZZQANumberMsg() {}

type zzqaNumberMsg interface{ isZZQANumberMsg() }

// A message json.Marshal accepts but the ledger's jsonb column cannot store
// must be refused by the codec with a cairn error naming the value, like every
// other value the ledger cannot keep, rather than fail the insert with a raw
// Postgres SQLSTATE.
func TestAMessageNumberJSONBCannotStoreIsRefusedBeforeTheInsert(t *testing.T) {
	t.Parallel()
	for name, msg := range map[string]zzqaNumberMsg{
		"an opaque payload": zzqaRawNumberPayload{Payload: json.RawMessage(`{"n":1e200000}`)},
		"a json.Number":     zzqaNumberLiteral{N: json.Number("1e200000")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// The premise: json.Marshal writes this value, so the only thing
			// that can refuse it is jsonb's own numeric range. The codec must
			// refuse it first; accepting it is the defect, and the insert is
			// then the first thing to say so.
			if _, err := json.Marshal(msg); err != nil {
				t.Fatalf("json.Marshal refuses this value, so it is not the case under test: %v", err)
			}
			if err := codec.CheckStorable(msg); err == nil {
				t.Fatal("the codec accepts a value jsonb cannot store, so the insert is the first thing to refuse it")
			}

			msgs := codec.New[zzqaNumberMsg]()
			codec.Register[zzqaRawNumberPayload](msgs, "zzqa.raw.number.msg", 1)
			codec.Register[zzqaNumberLiteral](msgs, "zzqa.number.literal.msg", 1)
			results := codec.New[int]()
			codec.Register[int](results, "zzqa.number.result", 1)
			agg := &ledger.Aggregate[struct{}, zzqaNumberMsg, int, struct{}, struct{}]{
				Store: ledgertest.NewStore(t),
				Kind:  "zzqa-number",
				Decider: cairn.Decider[struct{}, zzqaNumberMsg, int, struct{}, struct{}]{
					Initial: func() struct{} { return struct{}{} },
					Decide: func(struct{}, zzqaNumberMsg) cairn.Decision[int, struct{}, struct{}] {
						return cairn.Accept[int, struct{}, struct{}](1, nil)
					},
					Evolve: func(s struct{}, _ struct{}) struct{} { return s },
				},
				Codecs: &codec.Set[zzqaNumberMsg, int, struct{}, struct{}]{
					Messages: msgs,
					Results:  results,
					Events:   codec.New[struct{}](),
					Effects:  codec.New[struct{}](),
				},
			}

			_, err := agg.Handle(ctx, "a", msg)
			if err == nil {
				t.Fatal("the message was recorded although its JSON number cannot be stored")
			}
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) {
				t.Fatalf("the caller gets a raw database error instead of a cairn error naming the value: %v", err)
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("Handle failed with %v, want a *codec.NotStorableError naming the number", err)
			}
			if !strings.Contains(notStorable.Path, "value.") {
				t.Fatalf("the refusal names %q, want the field path", notStorable.Path)
			}
		})
	}
}
