package main

// An upcaster is where a stored type's old entries stay readable. The manifest
// records that a codec has an upcaster from version N, but not which builds
// register it, so moving the Upcast call from an unconstrained file into a
// platform-tagged one leaves the module-wide manifest byte-identical and
// -base passes -- while a deployment on the platform that lost it can no
// longer decode the entries it already stored at that version.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQAUpcasterMovedBetweenBuildsFailsTheBaseCheck(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gomod := "module example.com/upcastmoved\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => " + root + "\n"

	// events.go registers version 2 of the codec and does not itself register
	// the upcaster from version 1, so the upcaster is the only thing that
	// decides whether an entry written at version 1 can be read.
	events := `package upcastmoved

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Created struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Created) isEvent() {}

var codecs = NewCodecs()

func Codecs() *codec.Codec[Event] { return codecs }

func NewCodecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Created](c, "moved.created", 2)
	return c
}
`

	upcast := func(constraint string) string {
		return constraint + `package upcastmoved

import "encoding/json"

func init() {
	Codecs().Upcast("moved.created", 1, func(raw json.RawMessage) (json.RawMessage, error) {
		return raw, nil
	})
}
`
	}

	// The previous release: version 2, with the upcaster from version 1
	// registered in every build.
	dir := writeModule(t, gomod, map[string]string{
		"events.go": events,
		"upcast.go": upcast(""),
	})
	base := filepath.Join(dir, "base.yaml")
	if err := run(dir, "base.yaml", "", []string{"./..."}); err != nil {
		t.Fatalf("writing the previous release's manifest: %v", err)
	}

	// Control: the previous release checked against its own manifest passes,
	// so a failure below is about the move and not about the fixture.
	if err := run(dir, "same.yaml", base, []string{"./..."}); err != nil {
		t.Fatalf("the previous release must pass against its own manifest: %v", err)
	}

	// The new release moves the upcaster into a file built only on Windows.
	// On every other platform the codec now has no path from version 1 to 2.
	if err := os.Remove(filepath.Join(dir, "upcast.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "upcast_windows.go"), []byte(upcast("//go:build windows\n\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	// Check what the extractor says: it reads the Windows file in a Windows
	// build, so the module-wide manifest still lists the upcaster and is
	// byte-identical to the base. That is why -base cannot see the move.
	old, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("extracting the new release: %v", err)
	}
	raw, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	now, err := encode(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(now) != string(raw) {
		t.Fatalf("the two releases' manifests differ, so this reproduction is not testing a build-membership change:\n%s", now)
	}

	if err := run(dir, "new.yaml", base, []string{"./..."}); err == nil {
		t.Fatal("moving the upcaster from version 1 into a Windows-only file passed -base, though every other platform can no longer decode the entries it already stored at version 1")
	} else if !strings.Contains(err.Error(), "moved.created") {
		t.Fatalf("-base failed for a reason that does not name the codec: %v", err)
	}

	// Control: removing the upcaster from every build is still caught, so a
	// fix cannot pass by failing every base.
	if err := os.Remove(filepath.Join(dir, "upcast_windows.go")); err != nil {
		t.Fatal(err)
	}
	if err := run(dir, "new.yaml", base, []string{"./..."}); err == nil ||
		!strings.Contains(err.Error(), "no upcaster from version 1") {
		t.Fatalf("removing the upcaster from every build must still fail with the missing version, got %v", err)
	}
}
