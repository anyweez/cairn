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

// wrongMarshaler has a method named MarshalText that does not satisfy
// encoding.TextMarshaler (it takes an argument), so encoding/json ignores it
// and writes the field. It exercises the same fallback as the wrongly-signed
// MarshalJSON below; go vet's stdmethods check rejects a wrongly-signed
// MarshalJSON in compiled code, so the runtime reference uses MarshalText.
type wrongMarshaler struct {
	A int `json:"a"`
}

func (wrongMarshaler) MarshalText(extra int) ([]byte, error) { return nil, nil }

// opaque() must decide a marshaler from its signature, not its name alone. A
// method named MarshalJSON whose signature does not satisfy json.Marshaler
// makes encoding/json fall back to the type's fields, so the manifest must not
// call the type opaque: doing so hides a field-shape change from the -base
// check.
func TestAWrongSignatureMarshalJSONDoesNotMakeATypeOpaque(t *testing.T) {
	// Reference: encoding/json writes the field because a wrongly-signed
	// marshaler method is not the interface it is named for.
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

// fields() decides a type is opaque from the marshaler method's signature, so
// only the exact func() ([]byte, error) shape counts. json.RawMessage is a
// named []byte and does not satisfy json.Marshaler, and encoding/json writes
// the fields for it too.
func TestOnlyAMarshalerSignatureMakesATypeOpaque(t *testing.T) {
	cases := []struct {
		name string
		meth string
		want []string
	}{
		{"correct MarshalJSON is opaque", "func (T) MarshalJSON() ([]byte, error) { return nil, nil }", []string{"=: p.T"}},
		{"correct MarshalText is opaque", "func (T) MarshalText() ([]byte, error) { return nil, nil }", []string{"=: p.T"}},
		{"json.RawMessage result is not opaque", "func (T) MarshalJSON() (RawMessage, error) { return nil, nil }", []string{"a: int"}},
		{"single result is not opaque", "func (T) MarshalJSON() []byte { return nil }", []string{"a: int"}},
		{"argument is not opaque", "func (T) MarshalJSON(extra int) ([]byte, error) { return nil, nil }", []string{"a: int"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := `package p

type RawMessage []byte

type T struct {
	A int ` + "`json:\"a\"`" + `
}

` + tc.meth + "\n"
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
			if !slices.Equal(got, tc.want) {
				t.Errorf("fields() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The ",string" option is ignored on a type that marshals itself: the field
// is written by its marshaler, which never wraps its output in a string. The
// manifest rendered string(type) instead, disagreeing with what is stored.
type marshalerString string

func (marshalerString) MarshalText() ([]byte, error) { return []byte("Ts"), nil }

type stringOptionMarshaler struct {
	F marshalerString `json:"f,string"`
}

func TestAStringOptionOnAMarshalerTypeIsIgnored(t *testing.T) {
	// Reference: the marshaler's output is written as-is, not quoted again.
	raw, err := json.Marshal(stringOptionMarshaler{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"f":"Ts"}` {
		t.Fatalf("reference: encoding/json wrote %s, want {\"f\":\"Ts\"}", raw)
	}

	declared := loadDeclaredTypes(t)
	got := fields(declared.get(t, "stringOptionMarshaler"))
	want := []string{"f: " + types.TypeString(declared.get(t, "marshalerString"), nil)}
	if !slices.Equal(got, want) {
		t.Errorf("fields() = %q; the option is ignored, so want %q", got, want)
	}
}
