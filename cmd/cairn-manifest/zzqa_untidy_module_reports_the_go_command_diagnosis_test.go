package main

// The tool's package doc and the README's "The spec manifest" section both
// promise that a module it cannot load is reported with the go command's own
// message: "a module it can't load at all — a go.mod that needs tidying, say:
// the tool fails with go's own message rather than writing a manifest that
// says the module declares nothing."
//
// A module that needs tidying is the state a developer is in the moment they
// add an import, which is exactly when they run this command. The go command
// names the fix:
//
//	untidy.go:3:8: missing go.sum entry for module providing package
//	go.yaml.in/yaml/v3 (imported by example.com/untidy); to add:
//		go get example.com/untidy
//
// The extractor drops that diagnosis. `go list` records it in the package's
// DepsErrors, which go/packages keeps in an unexported field, so what reaches
// the extractor is only the type checker's report of the unresolvable import —
// `could not import go.yaml.in/yaml/v3 (invalid package name: "")` — plus one
// `undefined:` line for each use of the package. A reader is told an import
// has an invalid package name, not that go.mod is missing a checksum.
//
// The test pins the documented behaviour, not a wording: the error must carry
// the go command's diagnosis that the imported package's module is missing.

import (
	"strings"
	"testing"
)

func TestQAAnUntidyModuleReportsTheGoCommandDiagnosis(t *testing.T) {
	// go.yaml.in/yaml/v3 is this tool's own dependency, so the module cache
	// already holds it and the fixture needs no network: the missing go.sum
	// entry is what the go command reports whatever the proxy would say.
	t.Setenv("GOPROXY", "off")
	dir := writeModule(t, "module example.com/untidy\n\ngo 1.26.0\n\nrequire go.yaml.in/yaml/v3 v3.0.5\n", map[string]string{
		"untidy.go": `package untidy

import "go.yaml.in/yaml/v3"

var _ = yaml.Marshal
`,
	})
	_, err := Extract(dir, "./...")
	if err == nil {
		t.Fatal("a module whose import has no go.sum entry was read; want an error")
	}
	if !strings.Contains(err.Error(), "missing go.sum entry") {
		t.Fatalf("the error does not carry the go command's diagnosis of the missing module:\n%s", err)
	}
}
