package main

import (
	"encoding/json"
	"go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

// numberField and stringField hold the same key, "n", and differ only in the
// value's JSON kind: encoding/json writes a json.Number as a bare number and a
// string as a quoted one, and an entry stored as a number cannot be decoded
// into a string field.
type numberField struct {
	N json.Number `json:"n"`
}

type stringField struct {
	N string `json:"n"`
}

// fields() promises the JSON shape "as encoding/json writes it". It shapes a
// json.Number as the basic kind underneath it, "string", which is exactly the
// shape a string field has. A change between the two therefore leaves the
// manifest identical, so the -base check stays silent while entries already in
// the ledger stop decoding.
func TestAJSONNumberFieldIsNotShapedAsAString(t *testing.T) {
	numberType := lookedUpType(t, "numberField")
	stringType := lookedUpType(t, "stringField")

	// The reference: encoding/json tells the two apart on the wire.
	numberJSON, err := json.Marshal(numberField{N: "1"})
	if err != nil {
		t.Fatal(err)
	}
	stringJSON, err := json.Marshal(stringField{N: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(numberJSON) == string(stringJSON) {
		t.Fatalf("the fixtures do not differ on the wire: both %s", numberJSON)
	}
	var into stringField
	if err := json.Unmarshal(numberJSON, &into); err == nil {
		t.Fatalf("a json.Number entry %s decoded into a string field, so the change is not a wire break", numberJSON)
	}

	number, str := fields(numberType), fields(stringType)
	if slices.Equal(number, str) {
		t.Fatalf("fields() renders a json.Number field as %v, the same as a string field; a change between them must not leave the shape identical", number)
	}

	old := &Manifest{Codecs: []Codec{{Sum: "example.com/x.Msg", Name: "x.n", Version: 1, Fields: number}}}
	now := &Manifest{Codecs: []Codec{{Sum: "example.com/x.Msg", Name: "x.n", Version: 1, Fields: str}}}
	if problems := Incompatible(old, now); len(problems) == 0 {
		t.Fatal("Incompatible reports nothing for a json.Number field becoming a string, so a version-less wire break passes the -base check")
	}
}

// lookedUpType returns the declared type of the package-level type with this
// name, as the extractor sees it.
func lookedUpType(t *testing.T, name string) types.Type {
	t.Helper()
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo, Dir: ".", Tests: true}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pkgs {
		if p.Types == nil {
			continue
		}
		if obj := p.Types.Scope().Lookup(name); obj != nil {
			return obj.Type()
		}
	}
	t.Fatalf("%s not found in the loaded package", name)
	return nil
}
