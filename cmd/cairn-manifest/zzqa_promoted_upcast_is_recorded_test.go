package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// A program may put its codec behind a small wrapper type that embeds
// *codec.Codec[T], so the wrapper can offer helpers of its own. Upcast is then
// called through the promoted method: wrapper.Upcast(name, from, f). The
// extractor only recognises a call whose receiver is *codec.Codec[T], so that
// upcaster never reaches the manifest, and the -base check reports a version
// bump as having no upcaster from the old version even though the built codec
// has one.
func TestQAPromotedUpcastIsRecorded(t *testing.T) {
	dir := t.TempDir()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod := fmt.Sprintf("module example.com/promoted\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => %s\n", root)
	src := `package promoted

import (
	"encoding/json"

	"github.com/anyweez/cairn/codec"
)

type Event interface{ isEvent() }

type V1 struct {
	X int ` + "`json:\"x\"`" + `
}

func (V1) isEvent() {}

// Events wraps the codec so the program can add helpers beside it.
type Events struct {
	*codec.Codec[Event]
}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[V1, Event](c, "promoted.event", 2)
	wrapper := &Events{Codec: c}
	// Promoted through the embedded field: the built codec has this upcaster.
	wrapper.Upcast("promoted.event", 1, func(raw json.RawMessage) (json.RawMessage, error) {
		return raw, nil
	})
	return c
}
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "promoted.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	if got, want := m.Codecs[0].Upcasts, []int{1}; !slices.Equal(got, want) {
		t.Fatalf("the upcaster registered through the promoted method is missing from the manifest: upcasts %v, want %v", got, want)
	}
}
