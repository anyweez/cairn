package codec_test

import (
	"encoding/json"
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
		"a genuine replacement rune":      "\"a\ufffdb\"",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Encode(verbatimJSONHolder{V: sent}); err != nil {
				t.Fatalf("a storable marshalled value was refused: %v", err)
			}
		})
	}
}

// A replacement-character escape is not a byte jsonb refuses: jsonb stores it
// happily, as a U+FFFD the caller never sent. json writes a genuine U+FFFD raw
// as its three UTF-8 bytes and escapes no other rune as \ufffd, so the escape
// is always some encoder's stand-in for a byte it could not decode, and a
// marshaler that emits it has already laundered its input.
func TestEncodeRefusesAReplacementCharacterAnEncoderWrote(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[verbatimJSONHolder](c, "verbatimJSONHolder", 1)

	for name, sent := range map[string]verbatimJSON{
		"a replacement-character escape in a string":      `"a\ufffdb"`,
		"a replacement-character escape in an object key": `{"a\ufffd":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := c.Encode(verbatimJSONHolder{V: sent})
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError: the entry would hold U+FFFD where the caller's byte was", err)
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

// The customary custom marshaler copies its type and hands the copy to
// json.Marshal. That inner encoder replaces each invalid byte with the escape
// \ufffd, so what comes back is valid JSON naming a byte the caller never
// sent — the laundering CheckStorable exists to prevent, reaching the entry
// through the one path that looked safe.
type innerMarshalJSON struct {
	Name string
}

func (v innerMarshalJSON) MarshalJSON() ([]byte, error) {
	type plain innerMarshalJSON
	return json.Marshal(plain(v))
}

type innerMarshalJSONHolder struct {
	V innerMarshalJSON `json:"v"`
}

func (innerMarshalJSONHolder) area() int { return 0 }

func TestEncodeRefusesInvalidUTF8LaunderedByAnInnerMarshal(t *testing.T) {
	sent := innerMarshalJSONHolder{V: innerMarshalJSON{Name: "a\xffb"}}
	if err := codec.CheckStorable(sent); err == nil {
		t.Fatal("CheckStorable accepted a string whose invalid byte an inner json.Marshal turned into U+FFFD")
	}

	c := codec.New[shape]()
	codec.Register[innerMarshalJSONHolder](c, "innerMarshalJSONHolder", 1)
	_, err := c.Encode(sent)
	var notStorable *codec.NotStorableError
	if !errors.As(err, &notStorable) {
		t.Fatalf("got %v, want a NotStorableError", err)
	}
}
