package main

import (
	"slices"
	"testing"
)

// An import-path pattern that matches the whole module must read the same files
// as "./...": a package whose only file a build constraint excludes from the
// host is part of the module's spec, and its codecs, rules and tables belong in
// the manifest whichever spelling of the pattern selected the packages.
//
// unread() walks the module's Go files only when the pattern list contains the
// literal "./...", so the import-path spelling sees neither the package (which
// is absent from the host's build) nor an IgnoredFiles entry for it, and the
// extractor silently writes a manifest without the registration.
func TestQAImportPathPatternReadsBuildConstrainedPackage(t *testing.T) {
	dir := writeModule(t, `module example.com/pattern

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"common.go": `package pattern

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type Common struct{ A int }

func (Common) isEvent() {}

var Events = codec.New[Event]()

func init() { codec.Register[Common](Events, "pattern.common", 1) }
`,
		"platform/event_darwin.go": `package platform

import "github.com/anyweez/cairn/codec"

type Event interface{ isPlatformEvent() }

type Native struct{ N int }

func (Native) isPlatformEvent() {}

var Events = codec.New[Event]()

func init() { codec.Register[Native](Events, "pattern.platform", 1) }
`,
	})
	want := []string{"pattern.common", "pattern.platform"}

	// The module is valid: the per-directory spelling reads both registrations.
	dot, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("extracting with ./...: %v", err)
	}
	if got := codecNames(dot); !slices.Equal(got, want) {
		t.Fatalf("the fixture is wrong: ./... has codecs %v, want %v", got, want)
	}

	// The import-path spelling matches the same packages, so it must find the
	// same declarations.
	byPath, err := Extract(dir, "example.com/pattern/...")
	if err != nil {
		t.Fatalf("extracting with example.com/pattern/...: %v", err)
	}
	if got := codecNames(byPath); !slices.Equal(got, want) {
		t.Errorf("an import-path pattern silently left a build-constrained package out of the manifest: got %v, want %v", got, want)
	}
}
