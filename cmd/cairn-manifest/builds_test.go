package main

// The manifest is the module's spec, not the spec of the platform the tool runs
// on: a declaration behind a build constraint is read in a build that includes
// it, and a file no build includes is an error rather than a silent omission.

import (
	"bytes"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// constrainedModule writes a module whose declarations are split by build
// constraint: one of everything in every file, so a manifest that reads only
// the platform it was generated on is caught in each column.
func constrainedModule(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return writeModule(t, fmt.Sprintf("module example.com/constrained\n\ngo 1.26.0\n\nrequire github.com/anyweez/cairn v0.0.0\n\nreplace github.com/anyweez/cairn => %s\n", root), map[string]string{
		"events.go": `package events

import (
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

type Event interface{ isEvent() }

type Common struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Common) isEvent() {}

var CommonRule = spec.Rule{ID: "C1", Kind: spec.Can, Status: spec.Proposed, Statement: "Every build stores this."}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Common](c, "constrained.common", 1)
	return c
}
`,
		"linux.go": `//go:build linux

package events

import (
	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

type State string

const (
	Draft     State = "draft"
	Published State = "published"
)

var Lifecycle = fsm.New(
	fsm.Edge[State, string]{From: Draft, On: "publish", To: Published},
)

type LinuxPath struct {
	Path string ` + "`json:\"path\"`" + `
}

func (LinuxPath) isEvent() {}

var LinuxRule = spec.Rule{ID: "L1", Kind: spec.Never, Status: spec.Established, Statement: "Only a Linux build stores this."}

func init() {
	codec.Register[LinuxPath](Codecs(), "constrained.linux", 1)
}
`,
		"other.go": `//go:build !linux

package events

import (
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

type OtherPath struct {
	Path string ` + "`json:\"path\"`" + `
}

func (OtherPath) isEvent() {}

var OtherRule = spec.Rule{ID: "O1", Kind: spec.Never, Status: spec.Established, Statement: "Only a build that is not Linux stores this."}

func init() {
	codec.Register[OtherPath](Codecs(), "constrained.other", 1)
}
`,
		// A package whose only file is behind a tag: no ./... load on any
		// platform returns it, and no package names it as ignored.
		"tools/tools.go": `//go:build tools

package tools

import "github.com/anyweez/cairn/spec"

var ToolsRule = spec.Rule{ID: "T1", Kind: spec.Can, Status: spec.Proposed, Statement: "A tagged build stores this."}
`,
		// A package whose only file is for another platform than the one that
		// generates the manifest half the time.
		"onlydarwin/only.go": `//go:build darwin

package onlydarwin

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Path struct {
	Path string ` + "`json:\"path\"`" + `
}

func (Path) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Path](c, "constrained.darwin_only", 1)
	return c
}
`,
	})
}

func writeModule(t *testing.T, gomod string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = gomod
	for name, src := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func codecNames(m *Manifest) []string {
	names := make([]string, 0, len(m.Codecs))
	for _, c := range m.Codecs {
		names = append(names, c.Name)
	}
	slices.Sort(names)
	return names
}

func ruleIDs(m *Manifest) []string {
	ids := make([]string, 0, len(m.Rules))
	for _, r := range m.Rules {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids
}

// TestABuildConstrainedDeclarationIsInTheManifestOnEveryPlatform holds the
// manifest to the whole module: every codec, rule and table the source declares
// is in it whichever platform generated it, and the two manifests are the same
// file, so -base can never see the other platform's stored type as removed.
func TestABuildConstrainedDeclarationIsInTheManifestOnEveryPlatform(t *testing.T) {
	dir := constrainedModule(t)

	manifests := map[string]*Manifest{}
	for _, goos := range []string{"linux", "darwin"} {
		t.Setenv("GOOS", goos)
		m, err := Extract(dir, "./...")
		if err != nil {
			t.Fatalf("extracting for %s: %v", goos, err)
		}
		manifests[goos] = m
	}

	wantCodecs := []string{"constrained.common", "constrained.darwin_only", "constrained.linux", "constrained.other"}
	wantRules := []string{"C1", "L1", "O1", "T1"}
	for _, goos := range []string{"linux", "darwin"} {
		m := manifests[goos]
		if got := codecNames(m); !slices.Equal(got, wantCodecs) {
			t.Errorf("the manifest generated on %s has codecs %v, want %v", goos, got, wantCodecs)
		}
		if got := ruleIDs(m); !slices.Equal(got, wantRules) {
			t.Errorf("the manifest generated on %s has rules %v, want %v", goos, got, wantRules)
		}
		if len(m.Tables) != 1 || m.Tables[0].Name != "example.com/constrained.Lifecycle" {
			t.Errorf("the manifest generated on %s has tables %+v, want the one declared behind //go:build linux", goos, m.Tables)
		}
	}

	linux, err := encode(manifests["linux"])
	if err != nil {
		t.Fatal(err)
	}
	darwin, err := encode(manifests["darwin"])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(linux, darwin) {
		t.Errorf("one source tree produced two manifests:\n--- linux\n%s\n--- darwin\n%s", linux, darwin)
	}
}

// TestTheManifestRecordsTheBuildsThatRegisterAStoredType holds the manifest to
// the other half of the build search: reading every declaration is not enough
// when a stored type moves off a platform, because the module-wide list is then
// unchanged and -base would see no removal. Each codec records the builds its
// registration is written for, taken from the file so every host agrees.
func TestTheManifestRecordsTheBuildsThatRegisterAStoredType(t *testing.T) {
	dir := constrainedModule(t)
	want := map[string]string{
		"constrained.common":      "",       // an unconstrained file is in every build
		"constrained.linux":       "linux",  // //go:build linux
		"constrained.other":       "!linux", // //go:build !linux
		"constrained.darwin_only": "darwin",
	}
	for _, goos := range []string{"linux", "darwin"} {
		t.Setenv("GOOS", goos)
		m, err := Extract(dir, "./...")
		if err != nil {
			t.Fatalf("extracting for %s: %v", goos, err)
		}
		for _, c := range m.Codecs {
			if w, ok := want[c.Name]; ok && c.Build != w {
				t.Errorf("%s extracted on %s records build %q, want %q", c.Name, goos, c.Build, w)
			}
		}
	}
}

// A file name ties a file to a build only after a non-empty prefix: the go
// command reads foo_linux.go as Linux-only, and linux.go as a file for every
// build. A build line narrows the name further.
func TestFileBuildReadsTheFileNameAndTheBuildLine(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"foo_linux.go", "package x\n", "linux"},
		{"foo_linux_amd64.go", "package x\n", "linux && amd64"},
		{"foo_linux_test.go", "package x\n", "linux"},
		{"linux.go", "package x\n", ""},
		{"foo.go", "//go:build linux || darwin\n\npackage x\n", "linux || darwin"},
		{"foo_linux.go", "//go:build linux\n\npackage x\n", "linux"},
	} {
		path := filepath.Join(t.TempDir(), tc.name)
		if err := os.WriteFile(path, []byte(tc.src), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := fileBuild(path); got != tc.want {
			t.Errorf("%s: build %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The tags a build satisfies are the go command's rule, not this tool's: the
// target's GOOS and GOARCH, the OS they imply, and "unix" for a unix-like
// GOOS. buildTags carries a copy of go/build's unix table because the module
// cannot import it, so this compares that copy with go/build itself rather
// than with a second reading of the same docs.
func TestBuildTagsMatchTheGoCommand(t *testing.T) {
	goosTags, goarchTags := knownPlatformTags()
	probes := []string{"unix"}
	for tag := range goosTags {
		probes = append(probes, tag)
	}
	for tag := range goarchTags {
		probes = append(probes, tag)
	}
	slices.Sort(probes)

	// The tag is not the last _segment of the name, so the file name states no
	// constraint of its own and only the build line decides.
	dir := t.TempDir()
	for _, tag := range probes {
		src := "//go:build " + tag + "\n\npackage probe\n"
		if err := os.WriteFile(filepath.Join(dir, tag+"_probe.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, pair := range platformPairs() {
		ctxt := build.Context{GOOS: pair[0], GOARCH: pair[1], Compiler: "gc"}
		tags := buildTags(pair[0], pair[1])
		for _, tag := range probes {
			match, err := ctxt.MatchFile(dir, tag+"_probe.go")
			if err != nil {
				t.Fatalf("%s/%s: %v", pair[0], pair[1], err)
			}
			if match != tags[tag] {
				t.Errorf("%s/%s: go/build says %q is %t, buildTags says %t", pair[0], pair[1], tag, match, tags[tag])
			}
		}
	}
}

// TestAFileNoBuildIncludesIsRefused holds the other half of the contract: a
// constrained file the tool cannot read is an error naming the file and the
// constraint, and no manifest is written from it.
func TestAFileNoBuildIncludesIsRefused(t *testing.T) {
	dir := writeModule(t, `module example.com/unreadable

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"good.go": `package x

import "github.com/anyweez/cairn/spec"

var GoodRule = spec.Rule{ID: "G1", Kind: spec.Can, Status: spec.Proposed, Statement: "Read on every platform."}
`,
		"never.go": `//go:build never && !never

package x

import "github.com/anyweez/cairn/spec"

var NeverRule = spec.Rule{ID: "N1", Kind: spec.Can, Status: spec.Proposed, Statement: "No build reads this file."}
`,
	})

	err := run(dir, "spec.manifest.yaml", "", []string{"./..."})
	if err == nil {
		t.Fatal("a file no build includes was left out of the manifest instead of refused")
	}
	for _, want := range []string{"never.go", "//go:build never && !never"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, "spec.manifest.yaml")); !os.IsNotExist(statErr) {
		t.Errorf("a refused extraction wrote a manifest: %v", statErr)
	}
}

// A build that fails is not the whole answer: the same file is in other builds,
// and the one it belongs to may be later in the search. Here a_use.go is
// selected by either tag but only type-checks with both, so the first builds
// tried report an error and a later one reads it.
func TestABuildThatFailsDoesNotStopAnotherReadingTheFile(t *testing.T) {
	dir := writeModule(t, `module example.com/fallback

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
	codec.Register[Common](c, "constrained.common", 1)
	return c
}
`,
		// Sorted before z_def.go, so it is the file whose search has to survive
		// the builds that select it without the declaration it uses.
		"a_use.go": `//go:build cairnqa || cairnqa2

package events

import "github.com/anyweez/cairn/codec"

type Tagged struct {
	Path Detail ` + "`json:\"path\"`" + `
}

func (Tagged) isEvent() {}

func init() {
	codec.Register[Tagged](Codecs(), "constrained.tagged", 1)
}
`,
		"z_def.go": `//go:build cairnqa && cairnqa2

package events

type Detail struct {
	Text string ` + "`json:\"text\"`" + `
}
`,
	})

	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("a file that type-checks in one build was refused because another build failed: %v", err)
	}
	if got := codecNames(m); !slices.Equal(got, []string{"constrained.common", "constrained.tagged"}) {
		t.Errorf("codecs: %v", got)
	}
	for _, c := range m.Codecs {
		if c.Name == "constrained.tagged" && !slices.Equal(c.Fields, []string{"path: {text: string}"}) {
			t.Errorf("the tagged type's shape: %v", c.Fields)
		}
	}
}

func mustRepoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}
