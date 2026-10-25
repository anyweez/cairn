package main

import (
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

// declarations are this package's declared types, as the extractor sees them.
// The fixture types the shape tests compare against encoding/json are declared
// in _test.go files, so the load has to include tests.
type declarations map[string]types.Type

// loadDeclaredTypes loads the package once and returns every type it declares.
func loadDeclaredTypes(t *testing.T) declarations {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo, Dir: ".", Tests: true}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	out := declarations{}
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		for _, name := range p.Types.Scope().Names() {
			if obj := p.Types.Scope().Lookup(name); obj != nil && obj.Type() != nil {
				out[name] = obj.Type()
			}
		}
	}
	return out
}

// get returns the type declared under name, failing the test when there is
// none.
func (d declarations) get(t *testing.T, name string) types.Type {
	t.Helper()
	typ, ok := d[name]
	if !ok {
		t.Fatalf("%s not found in the loaded package", name)
	}
	return typ
}
