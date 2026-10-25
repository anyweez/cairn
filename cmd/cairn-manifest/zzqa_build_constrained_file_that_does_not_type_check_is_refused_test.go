package main

// A file the inherited build excludes is read in a build that includes it, and
// that second load can fail: the file may not type-check under the platform or
// tag that selects it (a symbol declared under a different constraint, a
// dependency that only resolves on that platform, a typo caught last by the
// cross-compile). readBuilds drops those errors -- loadBuild returns on
// packages.Load's error and never looks at p.Errors -- and extracts from the
// partial type information go/packages reports anyway.
//
// Two halves of the tool's contract break, both silently:
//
//   - a stored type whose field type the loader could not resolve is recorded
//     as "invalid type" and the manifest is written with exit 0. The package
//     doc says "Anything the manifest can't state exactly is an error rather
//     than a guess", and the same failure in the inherited load is reported.
//   - a codec.Register call the type checker could not instantiate has no
//     entry in Info.Instances, so codecCall reads the zero Instance's TypeArgs
//     and panics with a nil pointer dereference.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// qaTaggedModule writes a module with one registration in an unconstrained
// file and plat declares the tagged file the host build excludes. The tag is
// not a platform tag, so the file is read whatever host the test runs on.
func qaTaggedModule(t *testing.T, plat string) string {
	t.Helper()
	return writeModule(t, `module example.com/tagged

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go": `package events

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Common struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Common) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Common, Event](c, "tagged.common", 1)
	return c
}
`,
		"plat.go": plat,
	})
}

// qaExtract refuses to let a panic stand as a finding's failure: it reports
// the crash as the failure it is, with the message a fix run can act on.
func qaExtract(t *testing.T, dir string) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Extract panicked on a tagged file it could not type-check: %v", r)
		}
	}()
	_, err = Extract(dir, "./...")
	return err
}

// TestQABuildConstrainedFileThatDoesNotTypeCheckIsRefused holds the tagged
// load to the same answer the inherited one gives: a file the tool cannot read
// exactly is an error naming it, not a manifest and not a panic.
func TestQABuildConstrainedFileThatDoesNotTypeCheckIsRefused(t *testing.T) {
	t.Run("an unresolvable field type", func(t *testing.T) {
		dir := qaTaggedModule(t, `//go:build cairnqa

package events

import "github.com/anyweez/cairn/codec"

type PlatformPath struct {
	Path MissingType `+"`"+`json:"path"`+"`"+`
}

func (PlatformPath) isEvent() {}

func init() {
	codec.Register[PlatformPath, Event](Codecs(), "tagged.platform", 1)
}
`)
		err := run(dir, "spec.manifest.yaml", "", []string{"./..."})
		if err == nil {
			t.Fatalf("the tool wrote a manifest from a file it could not type-check; it records the field as \"invalid type\"")
		}
		if !strings.Contains(err.Error(), "plat.go") {
			t.Errorf("the refusal does not name the file it could not read: %v", err)
		}
		if !strings.Contains(err.Error(), "undefined: MissingType") {
			t.Errorf("the refusal does not carry the loader's message: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "spec.manifest.yaml")); !os.IsNotExist(statErr) {
			t.Errorf("a refused extraction wrote a manifest anyway: %v", statErr)
		}
	})

	t.Run("an unresolvable registration", func(t *testing.T) {
		dir := qaTaggedModule(t, `//go:build cairnqa

package events

import "github.com/anyweez/cairn/codec"

func init() {
	codec.Register[MissingType, Event](Codecs(), "tagged.platform", 1)
}
`)
		if err := qaExtract(t, dir); err == nil {
			t.Errorf("a registration the type checker could not instantiate was accepted instead of reported")
		} else if !strings.Contains(err.Error(), "plat.go") {
			t.Errorf("the refusal does not name the file it could not read: %v", err)
		} else if !strings.Contains(err.Error(), "undefined: MissingType") {
			t.Errorf("the refusal does not carry the loader's message: %v", err)
		}
	})
}
