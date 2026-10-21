package codec_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/anyweez/cairn/codec"
)

type qaAnyPayload struct {
	Payload any `json:"payload"`
}

type qaAnySum interface{ isQAAny() }

func (qaAnyPayload) isQAAny() {}

// A value stored with a json.Number past float64's exact range must come back
// as the same number. Encode writes it exactly; Decode unmarshals an any field
// through encoding/json, which turns a JSON number into a float64, so 2^53+1
// silently comes back as 2^53.
func TestQACodecAnyFieldKeepsALargeInteger(t *testing.T) {
	c := codec.New[qaAnySum]()
	codec.Register[qaAnyPayload](c, "qa.any", 1)

	const sent = int64(9007199254740993) // 2^53 + 1
	in := qaAnyPayload{Payload: json.Number("9007199254740993")}

	env, err := c.Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, back) {
		t.Fatalf("a value did not survive the codec: sent %#v, got %#v (want the number %d)", in, back, sent)
	}
}
