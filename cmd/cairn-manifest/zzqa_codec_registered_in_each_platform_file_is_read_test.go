package main

// A module may declare a stored type in one file per platform: the type and
// its codec.Register call live together in event_linux.go and event_windows.go,
// and each build sees exactly one of them. `go build ./...` and `go vet ./...`
// accept the module on either platform.
//
// The extractor reads every build the module has, so it sees both Register
// calls. Its duplicate check is keyed on the sum type and registered name
// alone, with no build membership, so the second call is reported as a
// duplicate of the first and the whole module is refused:
//
//	event_windows.go: platform.created is also registered at event_linux.go
//
// An upcaster registered in more than one build's file is merged rather than
// refused (UpcastBuilds records the union), and two registrations that say the
// same thing are no more ambiguous for the manifest than one. A valid module
// must still produce its manifest.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestQACodecRegisteredInEachPlatformFileIsRead(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gomod := fmt.Sprintf("module example.com/dupb\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => %s\n", root)
	shared := `package dupb

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

func Codecs() *codec.Codec[Event] { return codecs }

var codecs = codec.New[Event]()
`
	platform := func(build string) string {
		return "//go:build " + build + "\n\n" + `package dupb

import "github.com/anyweez/cairn/codec"

type Created struct {
	Path string ` + "`json:\"path\"`" + `
}

func (Created) isEvent() {}

func init() {
	codec.Register[Created, Event](Codecs(), "platform.created", 1)
}
`
	}
	for name, src := range map[string]string{
		"go.mod":           gomod,
		"events.go":        shared,
		"event_linux.go":   platform("linux"),
		"event_windows.go": platform("windows"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("a module that registers the same codec once per platform was refused: %v", err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want one platform.created", m.Codecs)
	}
	c := m.Codecs[0]
	if c.Name != "platform.created" {
		t.Errorf("name: got %q, want platform.created", c.Name)
	}
	if want := "linux || windows"; c.Build != want {
		t.Errorf("build: got %q, want %q, the union of the two files' constraints", c.Build, want)
	}
	if want := []string{"path: string"}; !slices.Equal(c.Fields, want) {
		t.Errorf("fields: got %q, want %q", c.Fields, want)
	}
}
