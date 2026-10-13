package good

import (
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

// Rules may live in tests, next to the checks that enforce them.
var InTests = spec.Rule{ID: "G3", Status: spec.Established, Statement: "Declared in a test file."}

// Codecs registered in tests are fixtures, not stored types.
func fixture() {
	codec.Register[Published_](codec.New[Event](), "good.fixture", 1)
}
