package main

import (
	"encoding/json"
	"go/types"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// jsonNumberSlice and jsonStringSlice have the same rendered shape today,
// but encoding/json writes them differently: a []json.Number is an array of
// bare numbers, a []string an array of quoted strings.
type jsonNumberSlice struct {
	Numbers []json.Number `json:"numbers"`
}

type jsonStringSlice struct {
	Numbers []string `json:"numbers"`
}

// fields() promises the JSON shape "as encoding/json writes it". A []json.Number
// is rendered as []string, the same as a []string, so the -base check watches a
// wire format that is not the one encoding/json writes.
func TestAJSONNumberSliceIsShapedAsEncodingJSONWritesIt(t *testing.T) {
	cfg := &packages.Config{Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo, Dir: ".", Tests: true}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(name string) types.Type {
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

	// The wire formats differ: bare numbers against quoted strings.
	numberRAW, err := json.Marshal(jsonNumberSlice{Numbers: []json.Number{"1"}})
	if err != nil {
		t.Fatal(err)
	}
	stringRAW, err := json.Marshal(jsonStringSlice{Numbers: []string{"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(numberRAW) == string(stringRAW) {
		t.Fatalf("the two wire formats are the same: %s", numberRAW)
	}

	number, str := fields(lookup("jsonNumberSlice")), fields(lookup("jsonStringSlice"))
	slices.Sort(number)
	slices.Sort(str)
	if strings.Join(number, ",") == strings.Join(str, ",") {
		t.Fatalf("fields() renders a []json.Number as %v, the same as a []string; a change between them must not leave the shape identical", number)
	}
}
