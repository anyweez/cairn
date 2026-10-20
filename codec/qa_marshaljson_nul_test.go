package codec_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// A value that decides its own JSON is opaque to CheckStorable ("those bytes
// are the JSON"), but json.Marshal does not make those bytes storable: a
// \u0000 escape is valid JSON that Postgres jsonb rejects (SQLSTATE 22P05),
// and bytes that are not valid UTF-8 survive json.Marshal's compaction and are
// rejected by jsonb as 22021. So Encode accepts a value whose entry can never
// be written, and the caller gets a raw SQLSTATE instead of a refusal.
type nulJSONValue string

func (v nulJSONValue) MarshalJSON() ([]byte, error) { return json.Marshal(string(v)) }

type nulJSONHolder struct {
	V nulJSONValue `json:"v"`
}

func (nulJSONHolder) area() int { return 0 }

// json.RawMessage is the standard library's own self-marshalling type, so a
// message that passes a payload through is exposed to the same thing.
type rawJSONHolder struct {
	P json.RawMessage `json:"p"`
}

func (rawJSONHolder) area() int { return 0 }

// A payload the ledger can store must still be accepted.
type fineJSONHolder struct {
	V nulJSONValue `json:"v"`
}

func (fineJSONHolder) area() int { return 0 }

// Encode refuses a value whose own JSON carries text jsonb cannot hold, rather
// than letting the write fail with a raw database error.
func TestEncodeRefusesAMarshalJSONValueTheLedgerCannotStore(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[nulJSONHolder](c, "nulJSONHolder", 1)
	codec.Register[rawJSONHolder](c, "rawJSONHolder", 1)

	for name, sent := range map[string]shape{
		"a marshalled NUL escape":  nulJSONHolder{V: "a\x00b"},
		"a raw NUL escape":         rawJSONHolder{P: json.RawMessage(`{"note":"a\u0000b"}`)},
		"a raw invalid UTF-8 byte": rawJSONHolder{P: json.RawMessage("{\"note\":\"a\xffb\"}")},
	} {
		t.Run(name, func(t *testing.T) {
			env, err := c.Encode(sent)
			if err == nil {
				t.Fatalf("Encode accepted %s, which jsonb cannot store, so the write fails with a raw SQLSTATE", env.Data)
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError", err)
			}
		})
	}
}

// A payload the ledger can keep is accepted and round-trips.
func TestEncodeChecksAMarshalJSONValueAsTheLedgerStoresIt(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[fineJSONHolder](c, "fineJSONHolder", 1)

	env, err := c.Encode(fineJSONHolder{V: "fine"})
	if err != nil {
		t.Fatalf("a storable JSON value was refused: %v", err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.(fineJSONHolder).V; got != "fine" {
		t.Fatalf("round-trip returned %q, want %q", got, "fine")
	}
}
