package codec_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// secretKey keeps its bytes in an unexported field and renders them through a
// pointer-receiver MarshalText. A slice element is addressable, so
// json.Marshal calls (*secretKey).MarshalText and then writes the bytes
// through its own string encoder. The walk skips the unexported field, so
// without the addressable rule it reports the value storable and the entry
// holds text the caller never sent.
type secretKey struct {
	raw []byte
}

func (k *secretKey) MarshalText() ([]byte, error)    { return k.raw, nil }
func (k *secretKey) UnmarshalText(text []byte) error { k.raw = text; return nil }

type secretKeys struct {
	Keys []secretKey `json:"keys"`
}

func (secretKeys) area() int { return 0 }

// The same shape with a []byte-kinded type. In a slice every element is
// addressable, so json calls the pointer-receiver MarshalText rather than
// base64-encoding the bytes.
type secretBytes []byte

func (b *secretBytes) MarshalText() ([]byte, error)    { return []byte(*b), nil }
func (b *secretBytes) UnmarshalText(text []byte) error { *b = text; return nil }

type secretByteHolder struct {
	Blobs []secretBytes `json:"blobs"`
}

func (secretByteHolder) area() int { return 0 }

// A storable value of the same shape: text the ledger can keep must still be
// accepted, and a nil pointer is storable because json writes null without
// calling MarshalText.
type storableRaw struct {
	raw []byte
}

func (k *storableRaw) MarshalText() ([]byte, error)    { return k.raw, nil }
func (k *storableRaw) UnmarshalText(text []byte) error { k.raw = text; return nil }

type storableRawHolder struct {
	Keys []storableRaw `json:"keys"`
}

func (storableRawHolder) area() int { return 0 }

// Encode refuses a value whose addressable pointer-receiver MarshalText yields
// text the ledger cannot store, rather than storing it and letting json.Marshal
// replace bytes the caller sent or Postgres reject the NUL.
func TestEncodeRefusesAnAddressablePointerMarshalTextValue(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[secretKeys](c, "secretKeys", 1)
	codec.Register[secretByteHolder](c, "secretByteHolder", 1)

	for name, sent := range map[string]shape{
		"an addressable struct with invalid UTF-8": secretKeys{Keys: []secretKey{{raw: []byte("bad\xff")}}},
		"an addressable struct with a NUL":         secretKeys{Keys: []secretKey{{raw: []byte("bad\x00")}}},
		"a []byte-kinded value with invalid UTF-8": secretByteHolder{Blobs: []secretBytes{secretBytes("bad\xff")}},
		"a []byte-kinded value with a NUL":         secretByteHolder{Blobs: []secretBytes{secretBytes("bad\x00")}},
	} {
		t.Run(name, func(t *testing.T) {
			env, err := c.Encode(sent)
			if err == nil {
				back, derr := c.Decode(env)
				if derr != nil {
					t.Fatal(derr)
				}
				t.Fatalf("Encode accepted and Decode returned %#v: the entry would hold text the caller never sent", back)
			}
			var notStorable *codec.NotStorableError
			if !errors.As(err, &notStorable) {
				t.Fatalf("got %v, want a NotStorableError", err)
			}
		})
	}
}

// A value the ledger can keep is accepted and round-trips through the pointer
// marshaler, and a nil pointer is not inspected because json writes null.
func TestEncodeChecksAnAddressablePointerMarshalTextValueAsJSONRendersIt(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[storableRawHolder](c, "storableRawHolder", 1)
	codec.Register[textPointerHolder](c, "textPointerHolder", 1)

	env, err := c.Encode(storableRawHolder{Keys: []storableRaw{{raw: []byte("fine")}}})
	if err != nil {
		t.Fatalf("a storable addressable text value was refused: %v", err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.(storableRawHolder).Keys[0].raw; string(got) != "fine" {
		t.Fatalf("round-trip returned %q, want %q", got, "fine")
	}

	if _, err := c.Encode(textPointerHolder{}); err != nil {
		t.Fatalf("a nil text pointer was refused: %v", err)
	}
}
