package codec_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// A type that decides its own JSON is not inspected as a Go value, but the
// bytes it returns are copied into the entry, so they get the check a string
// gets. jsonb rejects more of those bytes than JSON does: besides a NUL and
// bytes that are not valid UTF-8, it refuses a UTF-16 surrogate escape that is
// not part of a pair, with the same SQLSTATE 22P05.
type verbatimJSON string

func (v verbatimJSON) MarshalJSON() ([]byte, error) { return []byte(v), nil }

type verbatimJSONHolder struct {
	V verbatimJSON `json:"v"`
}

func (verbatimJSONHolder) area() int { return 0 }

// A nil pointer implementing json.Marshaler is storable: json.Marshal writes
// null and never calls the method, however unstorable the bytes it would
// return.
type pointerMarshaler struct{}

func (*pointerMarshaler) MarshalJSON() ([]byte, error) { return []byte(`"a\u0000b"`), nil }

type pointerMarshalerHolder struct {
	P *pointerMarshaler `json:"p"`
}

func (pointerMarshalerHolder) area() int { return 0 }

// Encode refuses a marshalled value whose escapes jsonb cannot decode, rather
// than letting the insert fail with a raw SQLSTATE.
func TestEncodeRefusesAnEscapeTheLedgerCannotStore(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[verbatimJSONHolder](c, "verbatimJSONHolder", 1)

	for name, sent := range map[string]verbatimJSON{
		"a NUL escape":           `"a\u0000b"`,
		"a high surrogate alone": `"\ud800"`,
		"a low surrogate alone":  `"\udc00"`,
		"a high surrogate before an escape that is not its pair": `"\ud800\u0041"`,
		"an unpaired surrogate in an object key":                 `{"a\ud800":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Encode(verbatimJSONHolder{V: sent})
			if err == nil {
				t.Fatalf("Encode accepted %s, which jsonb cannot store, so the write fails with a raw SQLSTATE", sent)
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError", err)
			}
		})
	}
}

// The escapes jsonb does decode are accepted, including an escaped backslash
// followed by the text "u0000", which is not an escape at all.
func TestEncodeAcceptsMarshalledEscapesTheLedgerCanStore(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[verbatimJSONHolder](c, "verbatimJSONHolder", 1)

	for name, sent := range map[string]verbatimJSON{
		"a surrogate pair":                `"\ud83d\ude00"`,
		"an escaped backslash then u0000": `"\\u0000"`,
		"an ordinary escape":              `"caf\u00e9"`,
		"plain JSON":                      `{"a":[1,true,null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Encode(verbatimJSONHolder{V: sent}); err != nil {
				t.Fatalf("a storable marshalled value was refused: %v", err)
			}
		})
	}
}

// A nil pointer is storable, and the same pointer once set is not: json calls
// the marshaller only for the value it actually writes.
func TestEncodeChecksANilPointerMarshalerAsNull(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[pointerMarshalerHolder](c, "pointerMarshalerHolder", 1)

	if _, err := c.Encode(pointerMarshalerHolder{}); err != nil {
		t.Fatalf("a nil marshaler pointer was refused: %v", err)
	}
	if _, err := c.Encode(pointerMarshalerHolder{P: &pointerMarshaler{}}); err == nil {
		t.Fatal("an unstorable marshalled value behind a pointer was encoded")
	}
}
