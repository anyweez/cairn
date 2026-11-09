package main

import (
	"slices"
	"testing"
)

// A module whose own import path ends in ".test" is legal Go: the suffix is
// only reserved for the synthetic main package the toolchain builds for a
// package's tests. The extractor skips every package whose PkgPath ends in
// ".test", so a real module named example.com/rules.test contributes nothing
// and its declaration behind no build constraint is then reported as unread.
func TestQATestSuffixedPackagePathIsRead(t *testing.T) {
	dir := writeModule(t, `module example.com/rules.test

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
		"rules.go": `package rules

import "github.com/anyweez/cairn/spec"

var Rule = spec.Rule{ID: "R1", Kind: spec.Can, Status: spec.Proposed, Statement: "This module's import path ends in .test and its rule is real."}
`,
	})

	m, err := Extract(dir, "./...")
	if err != nil {
		t.Fatalf("a real package whose import path ends in .test was not read: %v", err)
	}
	if got, want := ruleIDs(m), []string{"R1"}; !slices.Equal(got, want) {
		t.Errorf("rules: got %v, want %v; a package with a .test import path was skipped as if it were a generated test main", got, want)
	}
}
