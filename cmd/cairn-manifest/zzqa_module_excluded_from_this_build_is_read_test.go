package main

// A module's spec is the whole module, not the part of it this host's build
// selects: a file whose //go:build line or name ties it to another build is
// "read in a build that includes it", so "a tree therefore produces the same
// manifest on every host" (README, "The spec manifest"). A module whose every
// file is behind such a constraint is the same promise at the whole-module
// scale: this build has nothing to read, another build has all of it.
//
// The tag here is a plain custom one rather than another GOOS so the test does
// not depend on the host: no host sets qa_specs by itself, and `go build -tags
// qa_specs ./...` builds the module on every host.

import (
	"slices"
	"strings"
	"testing"
)

func TestQAModuleExcludedFromThisBuildIsRead(t *testing.T) {
	// The tag is the whole module's selection, so a GOFLAGS that happens to
	// enable it must not decide whether this test sees the defect.
	t.Setenv("GOFLAGS", "")

	dir := writeModule(t, `module example.com/hostexcluded

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		// The module's only file: no build this host makes by itself includes
		// it, and the build that does include it is valid.
		"events.go": `//go:build qa_specs

package events

import (
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

type Event interface{ isEvent() }

type Tagged struct {
	ID string ` + "`json:\"id\"`" + `
}

func (Tagged) isEvent() {}

var TaggedRule = spec.Rule{ID: "Q1", Kind: spec.Can, Status: spec.Proposed, Statement: "Only a build with qa_specs stores this."}

func Codecs() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[Tagged](c, "hostexcluded.tagged", 1)
	return c
}
`,
	})

	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("a valid module whose every file is behind the tag qa_specs was refused instead of read: %v", err)
	}
	if got, want := ruleIDs(m), []string{"Q1"}; !slices.Equal(got, want) {
		t.Errorf("rules: got %v, want %v", got, want)
	}
	if got, want := codecNames(m), []string{"hostexcluded.tagged"}; !slices.Equal(got, want) {
		t.Errorf("codecs: got %v, want %v", got, want)
	}

	// A module with no Go files at all is the case the refusal exists for: the
	// load read nothing, and there is no build that would read more. It must
	// keep failing, so the fix cannot simply drop the empty-load refusal.
	_, err = Extract("testdata/nopackages", "./...")
	if err == nil || !strings.Contains(err.Error(), "no packages match") {
		t.Errorf("a module with no Go files must still be refused with go's message, got: %v", err)
	}
}
