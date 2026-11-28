package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// A JSON key may contain an opening brace: encoding/json's isValidTag allows
// it. The manifest renders a field as "name: shape", and the -base check sorts
// the members of every struct in that string so a harmless reorder compares
// equal. The sort starts at the first "{" in the entry, which is inside the
// field's name here rather than at the nested struct's shape, so the members
// are left unsorted and a reorder is reported as a wire change.
type braceKeyOrderA struct {
	Meta struct {
		A int `json:"a"`
		B int `json:"b"`
	} `json:"x{y"`
}

type braceKeyOrderB struct {
	Meta struct {
		B int `json:"b"`
		A int `json:"a"`
	} `json:"x{y"`
}

func TestAJSONKeyWithAnOpeningBraceHidesNestedShapeOrder(t *testing.T) {
	// Reference: the two declarations write the same JSON object, only its
	// keys in another order.
	a, err := json.Marshal(braceKeyOrderA{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(braceKeyOrderB{})
	if err != nil {
		t.Fatal(err)
	}
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(av, bv) {
		t.Fatalf("the fixtures differ on the wire: %s and %s", a, b)
	}

	declared := loadDeclaredTypes(t)
	old := fields(declared.get(t, "braceKeyOrderA"))
	now := fields(declared.get(t, "braceKeyOrderB"))
	if len(old) == 0 || len(now) == 0 {
		t.Fatal("fields() returned no shape for the fixtures")
	}
	problems := Incompatible(
		&Manifest{Codecs: []Codec{{Sum: "example.com/x.Event", Name: "x.braced", Version: 1, Fields: old}}},
		&Manifest{Codecs: []Codec{{Sum: "example.com/x.Event", Name: "x.braced", Version: 1, Fields: now}}},
	)
	if len(problems) > 0 {
		t.Fatalf("reordering a nested struct's fields behind the JSON key %q was reported as a stored-shape change: %v; the two declarations write the same JSON, so a safe change fails -base", "x{y", problems)
	}
}
