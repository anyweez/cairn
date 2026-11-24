package main

import "testing"

// An unnamed interface sum may name its method parameters and results. go/types
// keeps those names in the type expression and reflect does not, so the runtime
// half of the cross-check names the same sum as codec.CheckRegistrations reads
// one correct codec as both missing from the manifest and extra in the codecs.
// The manifest therefore records the reflection's spelling: types only, with a
// nested function's parameter names dropped as well.
func TestAnUnnamedInterfaceSumIsNamedAsTheRuntimeNamesIt(t *testing.T) {
	m, err := Extract("testdata/ifacesum", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want the one registration declared in testdata/ifacesum", m.Codecs)
	}
	if got, want := m.Codecs[0].Sum, "interface{Accept(int, func(int) error) error}"; got != want {
		t.Fatalf("sum: the manifest records %q, but reflect drops parameter names: the runtime registration for the same declaration is %q", got, want)
	}
}
