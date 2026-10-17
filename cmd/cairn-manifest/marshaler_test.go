package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"slices"
	"testing"
)

// wrongMarshaler has a method named MarshalJSON that does not satisfy
// json.Marshaler (it takes an argument). It is not a json.Marshaler, so
// encoding/json writes its field.
type wrongMarshaler struct {
	A int `json:"a"`
}

func (wrongMarshaler) MarshalJSON(extra int) ([]byte, error) { return nil, nil }

// opaque() in shape.go recognises a marshaler by method name alone. A method
// named MarshalJSON whose signature does not satisfy json.Marshaler makes
// encoding/json fall back to the type's fields, so the manifest must not call
// the type opaque: doing so hides a field-shape change from the -base check.
func TestAWrongSignatureMarshalJSONDoesNotMakeATypeOpaque(t *testing.T) {
	// Reference: encoding/json writes the field because the method is not
	// json.Marshaler.
	b, err := json.Marshal(wrongMarshaler{A: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"a":1}` {
		t.Fatalf("reference: encoding/json wrote %s, want {\"a\":1}", b)
	}

	// The same declaration, loaded through go/types and shaped by fields().
	const src = `package p

type T struct {
	A int ` + "`json:\"a\"`" + `
}

func (T) MarshalJSON(extra int) ([]byte, error) { return nil, nil }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := (&types.Config{}).Check("p", fset, []*ast.File{f}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := fields(pkg.Scope().Lookup("T").Type())
	want := []string{"a: int"}
	if !slices.Equal(got, want) {
		t.Errorf("fields() = %q; encoding/json writes the field, so want %q", got, want)
	}
}
