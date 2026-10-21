package codec_test

import (
	"encoding/json"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// nilMarshaler is a value-receiver MarshalJSON that dereferences its receiver
// when it is called. A nil *nilMarshaler behind an interface is a value
// encoding/json writes as null without calling MarshalJSON.
type nilMarshaler string

func (v nilMarshaler) MarshalJSON() ([]byte, error) { return []byte(v), nil }

type ifaceJSONHolder struct {
	V any `json:"v"`
}

func (ifaceJSONHolder) area() int { return 0 }

// json.Marshal writes null for a nil Marshaler behind an interface and never
// calls MarshalJSON, so Encode must accept it without calling the method on
// the nil receiver.
func TestEncodeAcceptsANilMarshalJSONBehindAnInterface(t *testing.T) {
	sent := ifaceJSONHolder{V: (*nilMarshaler)(nil)}
	raw, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"v":null}` {
		t.Fatalf("encoding/json writes %s, want null", raw)
	}

	c := codec.New[shape]()
	codec.Register[ifaceJSONHolder](c, "ifaceJSONHolder", 1)

	env, err := c.Encode(sent)
	if err != nil {
		t.Fatalf("Encode refused a value encoding/json stores as null: %v", err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	// null carries no dynamic type, so nil — untyped or a nil pointer —
	// is what json gives back, here and on its own.
	if got := back.(ifaceJSONHolder).V; got != nil {
		if p, ok := got.(*nilMarshaler); !ok || p != nil {
			t.Fatalf("round-trip returned %#v, want nil", got)
		}
	}
}
