package codec_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
)

type shape interface{ area() int }

// Version 2 of square renamed Side to Edge.
type square struct {
	Edge int `json:"edge"`
}

func (s square) area() int { return s.Edge * s.Edge }

func TestOldVersionsAreUpcastBeforeDecoding(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[square](c, "square", 2)
	c.Upcast("square", 1, func(old json.RawMessage) (json.RawMessage, error) {
		var v1 struct {
			Side int `json:"side"`
		}
		if err := json.Unmarshal(old, &v1); err != nil {
			return nil, err
		}
		return json.Marshal(square{Edge: v1.Side})
	})

	got, err := c.Decode(codec.Envelope{Type: "square", Version: 1, Data: json.RawMessage(`{"side":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got.area() != 9 {
		t.Fatalf("decoded %+v", got)
	}

	env, err := c.Encode(square{Edge: 2})
	if err != nil || env.Version != 2 || env.Type != "square" {
		t.Fatalf("encoded %+v, %v", env, err)
	}
}

func TestDecodingRefusesWhatItCannotRead(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[square](c, "square", 2)

	for name, env := range map[string]codec.Envelope{
		"unknown type":     {Type: "circle", Version: 1, Data: json.RawMessage(`{}`)},
		"newer version":    {Type: "square", Version: 3, Data: json.RawMessage(`{"edge":1}`)},
		"missing upcaster": {Type: "square", Version: 1, Data: json.RawMessage(`{"side":1}`)},
		// A field the type no longer has means its shape changed without a
		// version bump; dropping it silently would lose data.
		"unknown field": {Type: "square", Version: 2, Data: json.RawMessage(`{"edge":1,"colour":"red"}`)},
	} {
		if _, err := c.Decode(env); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

func TestRegisterRejectsTypesOutsideTheSum(t *testing.T) {
	defer func() {
		if p := recover(); p == nil || !strings.Contains(p.(string), "does not implement") {
			t.Fatalf("expected a panic, got %v", p)
		}
	}()
	codec.Register[struct{ X int }](codec.New[shape](), "nope", 1)
}
