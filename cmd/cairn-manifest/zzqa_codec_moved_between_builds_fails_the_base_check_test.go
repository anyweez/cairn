package main

// A stored type can be moved from one platform's build to another between
// releases. The whole-module extraction still lists it, so the manifest is
// byte-for-byte the same; but on the platform whose build no longer registers
// it, entries written by the previous release can no longer be decoded.
// -base is the check that exists to catch exactly that, and it compares only
// the whole-module list, so the move passes silently.
//
// This is about presence per build, not about a field's shape: the type and
// its JSON shape are unchanged, and only the file that registers it moves.

import (
	"path/filepath"
	"strings"
	"testing"
)

const qaMovedEvents = `package moved

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type PlatformPath struct {
	Path string ` + "`json:\"path\"`" + `
}

func (PlatformPath) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	return c
}
`

const qaMovedLinux = `//go:build linux

package moved

import "github.com/anyweez/cairn/codec"

func init() {
	codec.Register[PlatformPath, Event](Codecs(), "moved.path", 1)
}
`

const qaMovedOther = `//go:build !linux

package moved

import "github.com/anyweez/cairn/codec"

func init() {
	codec.Register[PlatformPath, Event](Codecs(), "moved.path", 1)
}
`

const qaMovedEmpty = `//go:build linux

package moved
`

const qaMovedEmptyOther = `//go:build !linux

package moved
`

// qaWriteMovedModule writes the module with the registration in linux.go
// (moved=false) or in other.go (moved=true).
func qaWriteMovedModule(t *testing.T, moved bool) string {
	t.Helper()
	linux, other := qaMovedLinux, qaMovedEmptyOther
	if moved {
		linux, other = qaMovedEmpty, qaMovedOther
	}
	return writeModule(t, `module example.com/moved

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go": qaMovedEvents,
		"linux.go":  linux,
		"other.go":  other,
	})
}

func qaWriteManifest(t *testing.T, dir, name string) string {
	t.Helper()
	if err := run(dir, name, "", []string{"./..."}); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return filepath.Join(dir, name)
}

func TestQACodecMovedBetweenBuildsFailsTheBaseCheck(t *testing.T) {
	// The control: the same module checked against its own manifest passes.
	same := qaWriteMovedModule(t, false)
	base := qaWriteManifest(t, same, "before.yaml")
	if err := run(same, "same-after.yaml", base, []string{"./..."}); err != nil {
		t.Fatalf("an unchanged module must pass its own base: %v", err)
	}

	// The defect: the registration moves from linux.go to other.go. Nothing
	// about the stored type changed, but a Linux deployment at the new
	// release no longer registers moved.path, so its old entries cannot be
	// decoded. The generated manifests are identical, so -base reports the
	// tree as compatible.
	moved := qaWriteMovedModule(t, true)
	if err := run(moved, "moved-after.yaml", base, []string{"./..."}); err == nil {
		t.Error("moving a stored type's registration off linux passed -base, though a linux deployment can no longer decode the entries it already stored")
	} else if !strings.Contains(err.Error(), "moved.path") {
		t.Errorf("the base check failed for a different reason: %v", err)
	}

	// The control for the other direction: removing the registration from
	// every build is a removal, and -base must keep reporting it.
	removed := writeModule(t, `module example.com/removed

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go": qaMovedEvents,
		"other.go":  qaMovedEmptyOther,
	})
	if err := run(removed, "removed-after.yaml", base, []string{"./..."}); err == nil {
		t.Error("a stored type removed from every build must still fail -base")
	}
}
