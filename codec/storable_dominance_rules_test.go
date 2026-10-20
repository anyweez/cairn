package codec_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// A field with no tag and a field tagged with the same name, promoted from two
// embedded structs at the same depth. encoding/json prefers the tagged field,
// so only that field is stored.
type tagLoses struct {
	X string
}

type tagWins struct {
	X int `json:"X"`
}

type taggedAtEqualDepth struct {
	tagLoses
	tagWins
}

func (taggedAtEqualDepth) area() int { return 0 }

// The same JSON name at the same depth, both untagged. encoding/json drops the
// ambiguous name and writes neither field.
type tieA struct {
	X string
}

type tieB struct {
	X string
}

type tiedAtEqualDepth struct {
	tieA
	tieB
}

func (tiedAtEqualDepth) area() int { return 0 }

// An unexported embedded non-struct: encoding/json ignores the field even
// though its string carries text the ledger cannot store.
type hiddenText string

type unexportedEmbedded struct {
	hiddenText
}

func (unexportedEmbedded) area() int { return 0 }

// The other encoding/json field rules the storable-text walk has to follow.
// A value whose only unstorable text is in a field json does not write is
// accepted.
func TestEncodeResolvesFieldDominanceAsJSONDoes(t *testing.T) {
	for name, tc := range map[string]struct {
		value    shape
		register func(*codec.Codec[shape])
		want     string // what encoding/json writes
	}{
		"a tagged field beats an untagged one at the same depth": {
			value:    taggedAtEqualDepth{tagLoses: tagLoses{X: "bad\xff"}, tagWins: tagWins{X: 1}},
			register: func(c *codec.Codec[shape]) { codec.Register[taggedAtEqualDepth](c, "tagged", 1) },
			want:     `{"X":1}`,
		},
		"fields tied at the same depth are dropped": {
			value:    tiedAtEqualDepth{tieA: tieA{X: "bad\xff"}, tieB: tieB{X: "bad\xff"}},
			register: func(c *codec.Codec[shape]) { codec.Register[tiedAtEqualDepth](c, "tied", 1) },
			want:     `{}`,
		},
		"an unexported embedded non-struct is not stored": {
			value:    unexportedEmbedded{hiddenText: "bad\xff"},
			register: func(c *codec.Codec[shape]) { codec.Register[unexportedEmbedded](c, "unexportedEmbedded", 1) },
			want:     `{}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != tc.want {
				t.Fatalf("encoding/json writes %s, want %s", raw, tc.want)
			}
			c := codec.New[shape]()
			tc.register(c)
			if _, err := c.Encode(tc.value); err != nil {
				t.Fatalf("Encode refused text in a field encoding/json does not store: %v", err)
			}
		})
	}
}

// Resolving dominance must not stop the check from reaching the field that is
// stored: unstorable text in the surviving field is still refused, and named.
func TestEncodeRefusesTextInTheDominantField(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[domOuter](c, "domOuter", 1)

	_, err := c.Encode(domOuter{domInner: domInner{X: "fine"}, X: "bad\xff"})
	if err == nil {
		t.Fatal("Encode accepted unstorable text in the field encoding/json stores")
	}
	var notStorable *codec.NotStorableError
	if !errors.As(err, &notStorable) {
		t.Fatalf("got %v, want a NotStorableError", err)
	}
	if notStorable.Path != "value.X" {
		t.Fatalf("error names %q, want the stored field value.X", notStorable.Path)
	}
}
