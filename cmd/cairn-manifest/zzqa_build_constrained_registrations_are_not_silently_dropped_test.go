package main

// A registration in a file selected by a build constraint is part of the
// module's spec: on the platform that file is built for, the type is stored in
// the ledger. packages.Load only sees the files selected for the host's build
// context, and the extractor has no way to ask for another one, so the same
// source produces a different manifest on each platform. Neither half of the
// manifest check learns that anything was left out:
//
//   - the committed file silently omits a stored type, and a shape change to
//     it passes -base on a host where the file is not built;
//   - the -base check compares two manifests generated for different platforms
//     and reports the other platform's stored type as removed, failing a tree
//     that is correct on both.
//
// A manifest is an error rather than a guess, so the tool must either read the
// whole package or refuse with an error naming the file and its constraint.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// qaPlatformModule writes a module whose registrations are split by GOOS: one
// file registers on every platform, one only on Linux, one on everything else.
func qaPlatformModule(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": fmt.Sprintf("module example.com/platform\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => %s\n", root),
		"events.go": `package platform

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Common struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Common) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Common, Event](c, "platform.common", 1)
	return c
}
`,
		"linux.go": `//go:build linux

package platform

import "github.com/anyweez/cairn/codec"

type LinuxPath struct {
	Path string ` + "`json:\"path\"`" + `
}

func (LinuxPath) isEvent() {}

func init() {
	codec.Register[LinuxPath, Event](Codecs(), "platform.linux_path", 1)
}
`,
		"other.go": `//go:build !linux

package platform

import "github.com/anyweez/cairn/codec"

type OtherPath struct {
	Path string ` + "`json:\"path\"`" + `
}

func (OtherPath) isEvent() {}

func init() {
	codec.Register[OtherPath, Event](Codecs(), "platform.other_path", 1)
}
`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func qaCodecNames(m *Manifest) []string {
	names := make([]string, 0, len(m.Codecs))
	for _, c := range m.Codecs {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	return names
}

// extractFor loads dir as a build for goos, which is what a developer on that
// platform gets: the extractor passes no build configuration of its own.
func extractFor(t *testing.T, dir, goos string) (*Manifest, error) {
	t.Helper()
	t.Setenv("GOOS", goos)
	return Extract(dir, "./...")
}

func TestQABuildConstrainedRegistrationsAreNotSilentlyDropped(t *testing.T) {
	dir := qaPlatformModule(t)

	linux, linuxErr := extractFor(t, dir, "linux")
	darwin, darwinErr := extractFor(t, dir, "darwin")
	if linuxErr != nil || darwinErr != nil {
		// Refusing is the other acceptable answer: the tool says what it could
		// not read rather than writing a manifest that leaves it out.
		for _, err := range []error{linuxErr, darwinErr} {
			if err != nil && !strings.Contains(err.Error(), "linux.go") && !strings.Contains(err.Error(), "other.go") {
				t.Fatalf("refused without naming the file it could not read: %v", err)
			}
		}
		return
	}

	// Both files are part of the module. A manifest that omits either is a
	// guess about a stored type, and the two hosts disagree about the spec.
	if got, want := qaCodecNames(linux), []string{"platform.common", "platform.linux_path", "platform.other_path"}; !slices.Equal(got, want) {
		t.Fatalf("a manifest generated on linux drops a registration the source declares: got %v, want %v", got, want)
	}
	if got, want := qaCodecNames(darwin), []string{"platform.common", "platform.linux_path", "platform.other_path"}; !slices.Equal(got, want) {
		t.Fatalf("a manifest generated on darwin drops a registration the source declares: got %v, want %v", got, want)
	}

	// The -base check must not read a platform difference as a removal of a
	// stored type: the code still registers it where it is built.
	if problems := Incompatible(darwin, linux); len(problems) > 0 {
		t.Fatalf("-base reports a correct tree's stored types as broken because the manifests were generated for different platforms:\n  %s",
			strings.Join(problems, "\n  "))
	}
}
