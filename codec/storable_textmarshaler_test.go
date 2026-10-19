package codec_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// A value whose type implements encoding.TextMarshaler is not written by
// json.Marshal verbatim: json calls MarshalText and then passes the bytes
// through its own string encoder, which replaces bytes that are not valid
// UTF-8 with U+FFFD and escapes a NUL as \u0000. The type does not decide the
// final bytes, so Encode has to check them like any other string. This is the
// value-side twin of the map key that checkMapKey covers; json ignores a
// string-kind key's MarshalText but honours a value's.
type marshalTextValue string

func (v marshalTextValue) MarshalText() ([]byte, error) { return []byte(v), nil }

type textValueHolder struct {
	V marshalTextValue `json:"v"`
}

func (textValueHolder) area() int { return 0 }

// The same value behind a pointer. json renders the pointed-to value through
// MarshalText too, and a nil pointer as null without calling it.
type textPointerHolder struct {
	P *marshalTextValue `json:"p"`
}

func (textPointerHolder) area() int { return 0 }

// A text value the ledger can keep must still be accepted.
type fineTextValue string

func (v fineTextValue) MarshalText() ([]byte, error) { return []byte(v), nil }

type fineTextHolder struct {
	V fineTextValue `json:"v"`
}

func (fineTextHolder) area() int { return 0 }

// Encode refuses a value whose MarshalText yields text the ledger cannot
// store, rather than storing it and letting json.Marshal replace bytes the
// caller sent or Postgres reject the NUL.
func TestEncodeRefusesAMarshalTextValueThatIsNotStorable(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[textValueHolder](c, "textValueHolder", 1)

	for name, sent := range map[string]marshalTextValue{
		"invalid UTF-8": "bad\xff",
		"a NUL":         "bad\x00",
	} {
		t.Run(name, func(t *testing.T) {
			env, err := c.Encode(textValueHolder{V: sent})
			if err == nil {
				back, derr := c.Decode(env)
				if derr != nil {
					t.Fatal(derr)
				}
				t.Fatalf("Encode accepted %q and Decode returned %q: the entry would hold text the caller never sent", sent, back.(textValueHolder).V)
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError", err)
			}
		})
	}
}

// A text value the ledger can keep is accepted and round-trips, and a nil
// pointer is storable because json writes null and never calls MarshalText.
func TestEncodeChecksAMarshalTextValueAsJSONRendersIt(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[fineTextHolder](c, "fineTextHolder", 1)
	codec.Register[textPointerHolder](c, "textPointerHolder", 1)

	env, err := c.Encode(fineTextHolder{V: "fine"})
	if err != nil {
		t.Fatalf("a storable text value was refused: %v", err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.(fineTextHolder).V; got != "fine" {
		t.Fatalf("round-trip returned %q, want %q", got, "fine")
	}

	if _, err := c.Encode(textPointerHolder{}); err != nil {
		t.Fatalf("a nil text pointer was refused: %v", err)
	}

	sent := marshalTextValue("bad\xff")
	if _, err := c.Encode(textPointerHolder{P: &sent}); err == nil {
		t.Fatal("an unstorable text value behind a pointer was encoded")
	}
}
