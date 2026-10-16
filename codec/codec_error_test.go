package codec_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
)

type triangle struct{}

func (triangle) area() int { return 0 }

type labelled struct{ Note string }

func (labelled) area() int { return 0 }

// brokenMarshal decides its own encoding and fails at it.
type brokenMarshal struct{}

func (brokenMarshal) area() int { return 0 }

func (brokenMarshal) MarshalJSON() ([]byte, error) { return nil, errors.New("marshal refused") }

func passThrough(raw json.RawMessage) (json.RawMessage, error) { return raw, nil }

// Encode fails for a value outside the sum, for text the ledger cannot keep,
// and for a type whose own marshalling fails. Each is an error rather than a
// silently wrong envelope.
func TestEncodeRefusesWhatItCannotEncode(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[labelled](c, "labelled", 1)
	codec.Register[brokenMarshal](c, "broken", 1)

	if _, err := c.Encode(triangle{}); err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Errorf("an unregistered value: %v", err)
	}
	if _, err := c.Encode(labelled{Note: "ship\xffit"}); err == nil {
		t.Error("text the ledger cannot store was encoded")
	} else {
		var notStorable *codec.NotStorableError
		if !errors.As(err, &notStorable) {
			t.Errorf("got %v, want a NotStorableError", err)
		}
	}
	if _, err := c.Encode(brokenMarshal{}); err == nil || !strings.Contains(err.Error(), "marshal refused") {
		t.Errorf("a Marshaler that fails: %v", err)
	}
}

// A declaration mistake panics at startup rather than at the first entry: a
// duplicate name or type, a version below one, and an upcaster that has
// nothing to produce or no type to produce it for.
func TestRegisterAndUpcastRejectBadDeclarations(t *testing.T) {
	for name, declare := range map[string]func(){
		"duplicate name": func() {
			c := codec.New[shape]()
			codec.Register[square](c, "same", 1)
			codec.Register[circle](c, "same", 1)
		},
		"duplicate type": func() {
			c := codec.New[shape]()
			codec.Register[square](c, "one", 1)
			codec.Register[square](c, "two", 1)
		},
		"version below one": func() {
			codec.Register[square](codec.New[shape](), "square", 0)
		},
		"upcaster for an unregistered type": func() {
			codec.New[shape]().Upcast("missing", 1, passThrough)
		},
		"upcaster from the current version": func() {
			c := codec.New[shape]()
			codec.Register[square](c, "square", 1)
			c.Upcast("square", 1, passThrough)
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected a panic")
				}
			}()
			declare()
		})
	}
}

// An upcaster that fails is reported as such by Decode, Latest and DecodeAll
// alike, wrapping the upcaster's own error.
func TestUpcastersReportTheirOwnFailures(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[square](c, "square", 2)
	boom := errors.New("boom")
	c.Upcast("square", 1, func(json.RawMessage) (json.RawMessage, error) { return nil, boom })

	env := codec.Envelope{Type: "square", Version: 1, Data: json.RawMessage(`{"side":1}`)}
	if _, err := c.Decode(env); !errors.Is(err, boom) {
		t.Errorf("Decode: %v, want the upcaster's error", err)
	}
	if _, err := c.Latest(env); !errors.Is(err, boom) {
		t.Errorf("Latest: %v, want the upcaster's error", err)
	}
	if _, err := c.DecodeAll([]codec.Envelope{env}); !errors.Is(err, boom) {
		t.Errorf("DecodeAll: %v, want the upcaster's error", err)
	}
}
