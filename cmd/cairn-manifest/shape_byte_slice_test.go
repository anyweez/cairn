package main

import (
	"encoding/json"
	"go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

// byteMarshaler is a named byte type that chooses its own JSON encoding. A
// slice of it is therefore NOT base64: encoding/json only applies the
// byte-slice special case (base64) when the element type does not implement
// json.Marshaler or encoding.TextMarshaler.
type byteMarshaler byte

func (byteMarshaler) MarshalText() ([]byte, error) { return []byte("m"), nil }

type byteSliceHolder struct {
	Named []byteMarshaler `json:"named"`
}

// fields() promises the JSON shape "as encoding/json writes it". Its slice
// case returns "bytes" for any slice whose element's underlying type is
// uint8, without consulting the element's marshaler the way encoding/json
// does. So a []byteMarshaler is rendered as base64 when it is really a JSON
// array of the marshaled elements.
func TestASliceOfSelfMarshalingBytesIsNotBase64(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo, Dir: ".", Tests: true}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	var holder, elem types.Type
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		if obj := p.Types.Scope().Lookup("byteSliceHolder"); obj != nil {
			holder = obj.Type()
		}
		if obj := p.Types.Scope().Lookup("byteMarshaler"); obj != nil {
			elem = obj.Type()
		}
	}
	if holder == nil || elem == nil {
		t.Fatal("byteSliceHolder or byteMarshaler not found in the loaded package")
	}

	// The reference is what encoding/json actually writes for the slice.
	raw, err := json.Marshal([]byteMarshaler{1})
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || raw[0] != '[' {
		t.Fatalf("reference: encoding/json wrote %s, want a JSON array", raw)
	}

	want := []string{"named: []" + types.TypeString(elem, nil)}
	got := fields(holder)
	if !slices.Equal(got, want) {
		t.Fatalf("fields() = %q; encoding/json writes %s, so the manifest should be %q", got, raw, want)
	}
}
