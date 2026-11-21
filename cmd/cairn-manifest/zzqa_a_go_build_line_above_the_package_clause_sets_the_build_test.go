package main

// The go command applies a "//go:build" line wherever it appears in the
// leading comment run, including the comment group directly above the package
// clause: go/build's header scan collects every "//go:build" line before the
// package, and only the legacy "// +build" form is separated from the package
// documentation by a blank line. buildHeader skips the group that is the
// package doc, so a registration written in such a file is recorded with no
// build constraint at all -- the manifest says every build registers it, which
// is wider than the go command's answer.
//
// That is the damaging direction: -base only sees a registration narrow when
// the recorded constraint narrows, so a registration moved off the platform
// that really built the file passes the compatibility check, and the entries
// that platform already stored can no longer be decoded.
//
// The same header read goes wrong the other way too: a legacy "// +build" line
// that follows a block comment is not part of the header go/build applies (its
// scan stops at the first line that is not a "//" comment), while buildHeader
// clears its block flag at the blank line that ends the comment group and
// records the line. Both cases compare the recorded build with go/build's own
// answer for the file, so a pod can check either.

import (
	"fmt"
	"go/build"
	"path/filepath"
	"strings"
	"testing"
)

const qaDocBuildEvents = `package doc

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Common struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Common) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Common](c, "doc.common", 1)
	return c
}
`

// qaDocWriteModule writes a module whose only platform file is platformSrc.
// Its registration is doc.platform and its type is Platform.
func qaDocWriteModule(t *testing.T, platformSrc string) string {
	t.Helper()
	return writeModule(t, `module example.com/doc

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go":   qaDocBuildEvents,
		"platform.go": platformSrc,
	})
}

// qaDocPlatformSource writes its build line directly above the package clause.
// gofmt would insert a blank line there, but the go command applies the line as
// written: the file is in the build for goos and in no other.
func qaDocPlatformSource(goos string) string {
	return `//go:build ` + goos + `
package doc

import "github.com/anyweez/cairn/codec"

type Platform struct {
	Arch string ` + "`json:\"arch\"`" + `
}

func (Platform) isEvent() {}

func init() {
	codec.Register[Platform](Codecs(), "doc.platform", 1)
}
`
}

// qaLegacyBlockSource puts a legacy build line after a block comment. go/build
// stops its legacy scan at the block comment, so this file is in every build;
// the line constrains nothing.
func qaLegacyBlockSource(goos string) string {
	return `/* Copyright 2026 */

// +build ` + goos + `

package doc

import "github.com/anyweez/cairn/codec"

type Platform struct {
	Arch string ` + "`json:\"arch\"`" + `
}

func (Platform) isEvent() {}

func init() {
	codec.Register[Platform](Codecs(), "doc.platform", 1)
}
`
}

// qaDocRecordedBuild returns the build the manifest records for name, and
// whether the registration is there at all.
func qaDocRecordedBuild(t *testing.T, m *Manifest, name string) (string, bool) {
	t.Helper()
	for _, c := range m.Codecs {
		if c.Name == name {
			return c.Build, true
		}
	}
	return "", false
}

// TestQADocBuildLineIsTheConstraintTheGoCommandApplies holds the recorded
// build to go/build's answer for the file: for every platform the toolchain
// targets, the manifest's constraint selects the build exactly when the go
// command would build the file in it.
func TestQADocBuildLineIsTheConstraintTheGoCommandApplies(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"a //go:build line directly above the package clause", qaDocPlatformSource("windows")},
		{"a legacy +build line after a block comment", qaLegacyBlockSource("windows")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := qaDocWriteModule(t, tc.src)
			m, err := Extract(dir, "./...")
			if err != nil {
				t.Fatalf("extracting: %v", err)
			}
			recorded, ok := qaDocRecordedBuild(t, m, "doc.platform")
			if !ok {
				t.Fatalf("the registration is missing from the manifest: codecs %v", codecNames(m))
			}

			path := filepath.Join(dir, "platform.go")
			var wrong []string
			for _, pair := range platformPairs() {
				ctxt := build.Context{GOOS: pair[0], GOARCH: pair[1], Compiler: "gc"}
				built, err := ctxt.MatchFile(filepath.Dir(path), filepath.Base(path))
				if err != nil {
					t.Fatalf("%s/%s: %v", pair[0], pair[1], err)
				}
				if got := qaBuildSelects(t, recorded, pair[0], pair[1]); got != built {
					wrong = append(wrong, fmt.Sprintf("%s/%s: the manifest says registered = %t, the go command builds the file = %t", pair[0], pair[1], got, built))
				}
			}
			if len(wrong) > 0 {
				t.Errorf("the manifest records build %q for doc.platform, which is not the constraint the go command applies to platform.go on %d of %d platforms:\n  %s",
					recorded, len(wrong), len(platformPairs()), strings.Join(wrong, "\n  "))
			}
		})
	}
}

// TestQAMovingADocConstrainedRegistrationBetweenPlatformsFailsBase is the
// consequence: the file's build line changes from windows to darwin, so a
// windows deployment at the new release no longer registers the type and
// cannot decode the entries it already stored. Nothing about the codec
// changed, and -base is the check that exists to catch exactly this.
func TestQAMovingADocConstrainedRegistrationBetweenPlatformsFailsBase(t *testing.T) {
	// The control: the same module checked against its own manifest passes.
	same := qaDocWriteModule(t, qaDocPlatformSource("windows"))
	base := qaWriteManifest(t, same, "before.yaml")
	if err := run(same, "same-after.yaml", base, []string{"./..."}); err != nil {
		t.Fatalf("an unchanged module must pass its own base: %v", err)
	}

	moved := qaDocWriteModule(t, qaDocPlatformSource("darwin"))
	err := run(moved, "moved-after.yaml", base, []string{"./..."})
	if err == nil {
		t.Error("moving a stored type's registration from windows to darwin passed -base, though a windows deployment can no longer decode the entries it already stored")
	} else if !strings.Contains(err.Error(), "doc.platform") {
		t.Errorf("the base check failed for a different reason: %v", err)
	}
}
