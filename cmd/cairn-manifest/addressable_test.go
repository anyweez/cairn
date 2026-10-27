package main

// encoding/json reaches the fields of a value addressably when the value is a
// slice element, an array whose parent is addressable, or a struct behind a
// pointer. A field whose pointer type marshals itself is then opaque, so the
// manifest must record the type's name rather than expand its fields. The
// breadth-first walk in structFields carries addressability into an embedded
// struct, but the root entry was queued without the caller's flag, so a
// direct field of an addressable struct was shaped as if it were not.

import (
	"encoding/json"
	"go/types"
	"slices"
	"testing"
)

// addressableItem chooses its own JSON through a pointer receiver, so it is
// opaque only where encoding/json reaches a value addressably.
type addressableItem struct {
	Kept int `json:"kept"`
}

func (*addressableItem) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }

type addressableHolder struct {
	Item addressableItem `json:"item"`
}

type addressableOuter struct {
	Items []addressableHolder `json:"items"`
}

// A slice element is addressable, so the element's direct field keeps the
// pointer-receiver marshaler and is written as the type's own JSON. fields()
// expanded it instead, so a rename or retype of the field's fields looked like
// a stored-shape change and failed the -base check.
func TestAFieldInAnAddressableStructKeepsItsPointerMarshaler(t *testing.T) {
	// Reference: encoding/json applies (*addressableItem).MarshalJSON to the
	// slice element's field, so that field is "{}" and not {"kept":7}.
	raw, err := json.Marshal(addressableOuter{Items: []addressableHolder{{Item: addressableItem{Kept: 7}}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"items":[{"item":{}}]}` {
		t.Fatalf("reference: encoding/json wrote %s, want {\"items\":[{\"item\":{}}]}", raw)
	}

	declared := loadDeclaredTypes(t)
	outer := declared.get(t, "addressableOuter")
	item := declared.get(t, "addressableItem")

	got := fields(outer)
	want := []string{"items: []{item: " + types.TypeString(item, nil) + "}"}
	if !slices.Equal(got, want) {
		t.Fatalf("fields() = %q, want %q; the addressable field must be the opaque type name", got, want)
	}
}
