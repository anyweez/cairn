package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// invalidTagged has a field whose JSON tag name encoding/json rejects.
// encoding/json does not error on it: it falls back to the Go field name, so
// the stored key is "User", not "user's_name".
type invalidTagged struct {
	User int `json:"user's_name"`
	Ok   int `json:"ok"`
}

// fields() promises the JSON shape "as encoding/json writes it". It renders
// the rejected tag name instead of the Go field name, so the -base check
// watches a key that is never written.
func TestAFieldWithAnInvalidJSONTagIsShapedAsEncodingJSONWritesIt(t *testing.T) {
	ttype := loadDeclaredTypes(t).get(t, "invalidTagged")

	var got []string
	for _, f := range fields(ttype) {
		name, _, _ := strings.Cut(f, ":")
		got = append(got, strings.TrimSuffix(name, "?"))
	}

	raw, err := json.Marshal(invalidTagged{User: 1, Ok: 2})
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	var want []string
	for k := range obj {
		want = append(want, k)
	}
	slices.Sort(got)
	slices.Sort(want)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("fields() names %v; encoding/json writes %v", got, want)
	}
}
