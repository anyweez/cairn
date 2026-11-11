package main

// The build a stored type records must be the constraint the go command
// applies to the file its registration is written in: the manifest says which
// builds can decode the entries, and -base fails when that narrows, which is
// only meaningful if the recorded constraint is the real one.
//
// Two files make the recorded constraint differ from the go command's answer.
// A file constrained by more than one legacy "// +build" line is ANDed by the
// go command, but only the first line was read, so the manifest records a
// wider build than the file has. A file whose build line is not in its header
// at all -- here a raw string a generator writes -- is in every build, but the
// first line that parses as a constraint was read wherever it appears.
//
// Both are the same input read the same wrong way: the constraint is taken
// from one line of the file rather than from the header, as go/build reads it.

import (
	"fmt"
	"go/build"
	"path/filepath"
	"strings"
	"testing"
)

const qaBuildEvents = `package legacy

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Common struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Common) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Common](c, "legacy.common", 1)
	return c
}
`

// A file the go command builds only on linux/amd64, written in the legacy
// form: the two "// +build" lines are one ANDed constraint.
const qaLegacyTwoLines = `// +build linux
// +build amd64

package legacy

import "github.com/anyweez/cairn/codec"

type Platform struct {
	Arch string ` + "`json:\"arch\"`" + `
}

func (Platform) isEvent() {}

func init() {
	codec.Register[Platform](Codecs(), "legacy.platform", 1)
}
`

// A file the go command builds everywhere: the constraint-looking line is
// inside a raw string, not the file header. Nothing about it constrains the
// build, and gofmt leaves the file as it is.
const qaLegacyRawString = `package legacy

import "github.com/anyweez/cairn/codec"

type Platform struct {
	Arch string ` + "`json:\"arch\"`" + `
}

func (Platform) isEvent() {}

// fileTemplate is what this package's generator writes for each new event.
const fileTemplate = ` + "`" + `
//go:build linux

package legacy
` + "`" + `

func init() {
	codec.Register[Platform](Codecs(), "legacy.platform", 1)
}
`

// qaWriteBuildModule writes a module whose constrained file is src.
func qaWriteBuildModule(t *testing.T, src string) string {
	t.Helper()
	return writeModule(t, `module example.com/legacy

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go":   qaBuildEvents,
		"platform.go": src,
	})
}

// qaBuildSelects reports whether the recorded constraint selects the build
// goos/goarch describes. An empty constraint is every build.
func qaBuildSelects(t *testing.T, recorded, goos, goarch string) bool {
	t.Helper()
	if recorded == "" {
		return true
	}
	expr, err := parseBuild(recorded)
	if err != nil {
		t.Fatalf("the manifest records a build this tool cannot read back: %q: %v", recorded, err)
	}
	tags := buildTags(goos, goarch)
	return expr.Eval(func(tag string) bool { return tags[tag] })
}

// TestQABuildMembershipIsTheConstraintTheGoCommandApplies holds the recorded
// build to go/build's answer for the file: for every platform the toolchain
// targets, the manifest's constraint selects the build exactly when the go
// command would build the file in it.
func TestQABuildMembershipIsTheConstraintTheGoCommandApplies(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"two legacy +build lines the go command ANDs", qaLegacyTwoLines},
		{"a build line inside a raw string, not the header", qaLegacyRawString},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := qaWriteBuildModule(t, tc.src)
			m, err := Extract(dir, "./...")
			if err != nil {
				t.Fatalf("extracting: %v", err)
			}
			var recorded string
			for _, c := range m.Codecs {
				if c.Name == "legacy.platform" {
					recorded = c.Build
				}
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
				t.Errorf("the manifest records build %q for legacy.platform, which is not the constraint the go command applies to platform.go on %d of %d platforms:\n  %s",
					recorded, len(wrong), len(platformPairs()), strings.Join(wrong, "\n  "))
			}
		})
	}
}

// qaWriteLegacyPlatform writes the module with the legacy file's second line
// naming arch, so the registration is built only on linux/<arch>.
func qaWriteLegacyPlatform(t *testing.T, arch string) string {
	t.Helper()
	return writeModule(t, `module example.com/legacy

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go":   qaBuildEvents,
		"platform.go": strings.Replace(qaLegacyTwoLines, "amd64", arch, 1),
	})
}

// TestQAMovingALegacyConstrainedRegistrationBetweenPlatformsFailsBase is the
// consequence: the file's constraint changes from linux/amd64 to linux/arm64,
// so a linux/amd64 deployment at the new release no longer registers the type
// and cannot decode the entries it already stored. Nothing about the codec
// changed, and -base is the check that exists to catch exactly this.
func TestQAMovingALegacyConstrainedRegistrationBetweenPlatformsFailsBase(t *testing.T) {
	// The control: the same module checked against its own manifest passes.
	same := qaWriteLegacyPlatform(t, "amd64")
	base := qaWriteManifest(t, same, "before.yaml")
	if err := run(same, "same-after.yaml", base, []string{"./..."}); err != nil {
		t.Fatalf("an unchanged module must pass its own base: %v", err)
	}

	moved := qaWriteLegacyPlatform(t, "arm64")
	err := run(moved, "moved-after.yaml", base, []string{"./..."})
	if err == nil {
		t.Error("moving a stored type's registration from linux/amd64 to linux/arm64 passed -base, though a linux/amd64 deployment can no longer decode the entries it already stored")
	} else if !strings.Contains(err.Error(), "legacy.platform") {
		t.Errorf("the base check failed for a different reason: %v", err)
	}
}
