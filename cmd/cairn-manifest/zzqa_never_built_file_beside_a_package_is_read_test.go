package main

// The go command documents two layouts in which a file beside a package is
// deliberately part of no build: a generator tagged //go:build ignore, and the
// tools file tagged //go:build tools whose own package lists tool dependencies.
// `go build ./...` and `go vet ./...` accept such a directory, so the module is
// valid. The extractor's build search enables the tag, loads the whole
// directory, and refuses the module because the directory then holds two
// packages. A valid module must still produce its manifest.

import (
	"slices"
	"testing"
)

func TestQANeverBuiltFileBesideAPackageIsRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		src  string
	}{
		{
			name: "a generator behind the ignore tag",
			file: "gen.go",
			src: `//go:build ignore

package main

func main() {}
`,
		},
		{
			name: "a tools file in its own package",
			file: "tools.go",
			src: `//go:build tools

package tools

import _ "fmt"
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeModule(t, `module example.com/neverbuilt

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => `+mustRepoRoot(t)+`
`, map[string]string{
				"lib.go": `package neverbuilt

import "github.com/anyweez/cairn/spec"

var GoodRule = spec.Rule{ID: "G1", Kind: spec.Can, Status: spec.Proposed, Statement: "Read from the package."}
`,
				tc.file: tc.src,
			})

			m, err := Extract(dir, "./...")
			if err != nil {
				t.Fatalf("a module with %s cannot produce a manifest: %v", tc.name, err)
			}
			var ids []string
			for _, r := range m.Rules {
				ids = append(ids, r.ID)
			}
			if !slices.Contains(ids, "G1") {
				t.Errorf("the package's rule is missing from the manifest: %v", ids)
			}
		})
	}
}
