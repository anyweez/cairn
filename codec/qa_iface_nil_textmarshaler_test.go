package codec_test

import (
	"encoding/json"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// nilText is a value-receiver TextMarshaler that dereferences its receiver
// when it is called. It does not implement json.Marshaler, so a nil *nilText
// behind an interface is a value encoding/json writes as null without calling
// MarshalText.
type nilText string

func (v nilText) MarshalText() ([]byte, error) { return []byte(v), nil }

type ifaceTextHolder struct {
	V any `json:"v"`
}

func (ifaceTextHolder) area() int { return 0 }

// json.Marshal writes null for a nil text marshaler behind an interface and
// never calls MarshalText, so Encode must accept it. CheckStorable instead
// calls MarshalText on the nil receiver, which panics.
func TestEncodeAcceptsANilTextMarshalerBehindAnInterface(t *testing.T) {
	sent := ifaceTextHolder{V: (*nilText)(nil)}
	raw, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"v":null}` {
		t.Fatalf("encoding/json writes %s, want null", raw)
	}

	c := codec.New[shape]()
	codec.Register[ifaceTextHolder](c, "ifaceTextHolder", 1)

	env, err := c.Encode(sent)
	if err != nil {
		t.Fatalf("Encode refused a value encoding/json stores as null: %v", err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if got := back.(ifaceTextHolder).V; got != (*nilText)(nil) {
		t.Fatalf("round-trip returned %#v, want a nil pointer", got)
	}
}
