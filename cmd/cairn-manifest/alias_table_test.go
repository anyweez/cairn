package main

import "testing"

// A package-level fsm table or spec.Rule declared through an alias of the
// cairn type is legal Go and is a declaration the manifest is supposed to
// list. isNamed only accepts a *types.Named, so an alias is silently skipped:
// no error, and the declaration is missing from spec.manifest.yaml.
func TestExtractReadsDeclarationsWrittenWithAnAlias(t *testing.T) {
	m, err := Extract("testdata/aliastable", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 || m.Tables[0].Name != "example.com/aliastable.Lifecycle" {
		t.Fatalf("tables: got %+v, want the aliased Lifecycle table", m.Tables)
	}
	if len(m.Rules) != 1 || m.Rules[0].ID != "A1" {
		t.Fatalf("rules: got %+v, want the aliased A1 rule", m.Rules)
	}
}
