package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
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
	declared := loadDeclaredTypes(t)

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

	number, str := fields(declared.get(t, "jsonNumberSlice")), fields(declared.get(t, "jsonStringSlice"))
	slices.Sort(number)
	slices.Sort(str)
	if strings.Join(number, ",") == strings.Join(str, ",") {
		t.Fatalf("fields() renders a []json.Number as %v, the same as a []string; a change between them must not leave the shape identical", number)
	}
}
