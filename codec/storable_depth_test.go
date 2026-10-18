package codec_test

import (
	"testing"

	"github.com/anyweez/cairn/codec"
)

// A type deep enough that codec.Encode's storable-text walk stops early. The
// walk bounds its recursion at maxStorableDepth so a cycle can't overflow the
// stack, and returns "storable" past that bound. A value nested deeper than
// the bound therefore reaches json.Marshal unchecked, and json.Marshal
// replaces bytes that are not valid UTF-8 with U+FFFD.
type deepNode struct {
	Child *deepNode `json:"child,omitempty"`
	Text  string    `json:"text,omitempty"`
}

type deepSum interface{ deep() }

func (*deepNode) deep() {}

// The same shape the shallow cases use: a message carrying text the ledger
// cannot store is refused rather than quietly stored as different text. This
// one places the text past the walk's depth bound.
func TestEncodeRefusesTextDeeperThanTheStorableWalk(t *testing.T) {
	c := codec.New[deepSum]()
	codec.Register[*deepNode](c, "deep", 1)

	root := &deepNode{}
	cur := root
	for range 1200 {
		cur.Child = &deepNode{}
		cur = cur.Child
	}
	const sent = "bad\xff"
	cur.Text = sent

	env, err := c.Encode(root)
	if err != nil {
		return // refused: nothing is stored
	}
	back, err := c.Decode(env)
	if err != nil {
		t.Fatal(err)
	}
	got := back.(*deepNode)
	for got.Child != nil {
		got = got.Child
	}
	if got.Text != sent {
		t.Fatalf("Encode accepted %q and Decode returned %q: the entry would hold text the caller never sent", sent, got.Text)
	}
}
