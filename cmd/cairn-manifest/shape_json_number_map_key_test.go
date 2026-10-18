package main

import (
	"encoding/json"
	"slices"
	"testing"
)

// jsonNumberKeyMap and stringKeyMap hold the same key, "1", and differ only in
// the key's Go type. encoding/json writes every string-kind map key as a quoted
// JSON string, json.Number included, so the two are identical on the wire even
// though the same json.Number in a value position is a bare number.
type jsonNumberKeyMap struct {
	M map[json.Number]int `json:"m"`
}

type stringKeyMap struct {
	M map[string]int `json:"m"`
}

// jsonNumberValueMap holds a json.Number in a value position, where
// encoding/json does write a bare number.
type jsonNumberValueMap struct {
	M map[string]json.Number `json:"m"`
}

// A json.Number map key reaches shape() in the key position, where the
// json.Number special case must not apply: encoding/json quotes every
// string-kind key, so the rendered shape must stay "string" or a change between
// the two key types would look like a wire break the manifest reports and
// Incompatible flags. A json.Number value stays a number.
func TestAJSONNumberMapKeyIsShapedAsAString(t *testing.T) {
	numberRAW, err := json.Marshal(jsonNumberKeyMap{M: map[json.Number]int{"1": 2}})
	if err != nil {
		t.Fatal(err)
	}
	stringRAW, err := json.Marshal(stringKeyMap{M: map[string]int{"1": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if string(numberRAW) != string(stringRAW) {
		t.Fatalf("the fixtures differ on the wire: json.Number key %s, string key %s", numberRAW, stringRAW)
	}

	number, str := fields(lookedUpType(t, "jsonNumberKeyMap")), fields(lookedUpType(t, "stringKeyMap"))
	if !slices.Equal(number, str) {
		t.Fatalf("fields() renders a json.Number map key as %v, not the %v a string key gives; the two are identical on the wire", number, str)
	}

	value := fields(lookedUpType(t, "jsonNumberValueMap"))
	if want := []string{"m: map[string]number"}; !slices.Equal(value, want) {
		t.Fatalf("fields() renders map[string]json.Number as %v, want %v; a json.Number value must stay a number", value, want)
	}
}
