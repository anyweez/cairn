package codec_test

import (
	"testing"

	"github.com/anyweez/cairn/codec"
)

// domInner.X and domOuter.X share the JSON name "x", and domInner is embedded,
// so encoding/json keeps only the shallower domOuter.X and drops domInner.X
// entirely. Text in the dropped field is never written, so it is not a reason
// to refuse the value.
type domInner struct {
	X string `json:"x"`
}

type domOuter struct {
	domInner
	X string `json:"x"`
}

func (domOuter) area() int { return 0 }

func TestEncodeAcceptsTextJSONDropsAsShadowed(t *testing.T) {
	c := codec.New[shape]()
	codec.Register[domOuter](c, "domOuter", 1)

	v := domOuter{domInner: domInner{X: "bad\xff"}, X: "fine"}
	if _, err := c.Encode(v); err != nil {
		t.Fatalf("Encode refused a value whose only unstorable text is in a field encoding/json does not store: %v", err)
	}
}
