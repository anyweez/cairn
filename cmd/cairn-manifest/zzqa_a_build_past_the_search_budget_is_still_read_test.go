package main

// A file the inherited build excludes is read in a build that includes it. When
// the file type-checks in only one of those builds -- here a feature-tagged
// file that uses a constant declared for windows -- the search has to reach
// that build. readInBuild stops after maxBuildsPerFile (8) loads, and the
// working build here is the ninth platform the search visits (freebsd, with the
// feature tag), so the file is reported with the loader's "undefined" message
// from an unrelated platform even though a build reads it and it type-checks
// there.
//
// The module is valid: without the tag it builds on every host, and
// GOOS=freebsd go list -tags cairnqa reads every file with no errors.

import (
	"os"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestQABuildPastTheSearchBudgetIsStillRead(t *testing.T) {
	dir := writeModule(t, `module example.com/budget

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
	codec.Register[Common, Event](c, "budget.common", 1)
	return c
}
`,
		// The inherited build excludes this file: no build here sets cairnqa.
		// Sorted before bsd.go, so it is the file whose search has to reach the
		// build that declares BSDOnly.
		"a_use.go": `//go:build cairnqa

package events

import "github.com/anyweez/cairn/codec"

type Tagged struct {
	Path string ` + "`json:\"path\"`" + `
}

func (Tagged) isEvent() {}

func init() {
	_ = BSDOnly
	codec.Register[Tagged, Event](Codecs(), "budget.tagged", 1)
}
`,
		// Only a freebsd build with the tag reads both files together.
		"bsd.go": `//go:build cairnqa && freebsd

package events

const BSDOnly = 1
`,
	})

	// The fixture is a module a build really does read: prove that first, so a
	// failure below is the search not reaching the build rather than a broken
	// fixture.
	if _, err := loadPackages(&packages.Config{
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax,
		Dir:        dir,
		Tests:      true,
		Env:        append(os.Environ(), "GOOS=freebsd", "GOARCH=amd64"),
		BuildFlags: []string{"-tags=cairnqa"},
	}, "./..."); err != nil {
		t.Fatalf("the module does not load under GOOS=freebsd with the tag: %v", err)
	}

	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("a build reads the tagged file and it type-checks there, but the search never reached it: %v", err)
	}
	names := codecNames(m)
	for _, want := range []string{"budget.common", "budget.tagged"} {
		if !slices.Contains(names, want) {
			t.Errorf("the manifest is missing %s: got %v", want, names)
		}
	}
}
