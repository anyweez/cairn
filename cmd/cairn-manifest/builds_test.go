package main

// The manifest is the module's spec, not the spec of the platform the tool runs
// on: a declaration behind a build constraint is read in a build that includes
// it, and a file no build includes is an error rather than a silent omission.

import (
	"bytes"
	"fmt"
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

// A registered type's shape can depend on a build-constrained declaration in
// another package: here the codec lives in a, and the field's type is declared
// once per platform in b. Reading the constrained file is not enough -- the
// build that includes it has to resolve a's registration too, or the shape
// recorded is the host's and the two manifests disagree.
func TestAShapeThatDependsOnAnotherPackageIsRefused(t *testing.T) {
	dir := writeModule(t, `module example.com/crosspackage

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"a/events.go": `package a

import (
	"example.com/crosspackage/b"

	"github.com/anyweez/cairn/codec"
)

type Event interface{ isEvent() }

type Created struct {
	ID string  ` + "`json:\"id\"`" + `
	At b.Stamp ` + "`json:\"at\"`" + `
}

func (Created) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Created, Event](c, "cross.created", 1)
	return c
}
`,
		"b/stamp_linux.go": `//go:build linux

package b

type Stamp int64
`,
		"b/stamp_other.go": `//go:build !linux

package b

type Stamp string
`,
	})

	_, err := Extract(dir, "./...")
	if err == nil {
		t.Fatal("a stored type whose field is declared differently in another package's build was recorded as the host's shape")
	}
	for _, want := range []string{"Stamp", "stamp_linux.go", "stamp_other.go"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// A shape can change without the type's own declaration changing: a marshaler
// declared for one platform makes the type opaque there. On that platform the
// inherited build includes the marshaler, so the comparison needs a build that
// excludes it too, or the manifest records whichever host generated it.
func TestAShapeThatDependsOnABuildConstrainedMarshalerIsRefused(t *testing.T) {
	dir := writeModule(t, `module example.com/marshaler

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"events.go": `package marshaler

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Created struct {
	At Stamp ` + "`json:\"at\"`" + `
}

func (Created) isEvent() {}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Created, Event](c, "marshaler.created", 1)
	return c
}
`,
		"stamp.go": `package marshaler

type Stamp struct {
	A int ` + "`json:\"a\"`" + `
}
`,
		"stamp_linux.go": `//go:build linux

package marshaler

func (Stamp) MarshalJSON() ([]byte, error) { return []byte("1"), nil }
`,
	})

	for _, goos := range []string{"linux", "darwin"} {
		t.Setenv("GOOS", goos)
		_, err := Extract(dir, "./...")
		if err == nil {
			t.Fatalf("on %s: a type whose marshaler is declared for one platform was recorded as that platform's shape", goos)
		}
		for _, want := range []string{"Stamp", "stamp_linux.go"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("on %s: the refusal does not name %q: %v", goos, want, err)
			}
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
