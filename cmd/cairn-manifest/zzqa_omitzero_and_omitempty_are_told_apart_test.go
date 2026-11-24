package main

// omitempty and omitzero are not the same option. encoding/json omits a field
// tagged omitempty when its value is empty in the old sense (a zero-length
// array, map, slice or string, a false bool, a zero number, a nil pointer or
// interface), but never omits a zero struct or a non-zero-length array.
// omitzero omits any value whose IsZero method, or reflect's IsZero, reports
// zero, including a zero struct. So switching one option for the other on a
// struct-typed field changes the JSON encoding/json writes for the same value.
//
// fields() renders both options as the same "?" marker, so the committed
// manifest cannot show the switch and Incompatible sees no shape change. A
// stored type can change its wire format without a version bump and -base
// passes it.

import (
	"encoding/json"
	"slices"
	"testing"
)

type qaOmitInner struct {
	X int `json:"x"`
}

type qaOmitEmpty struct {
	A qaOmitInner `json:"a,omitempty"`
}

type qaOmitZero struct {
	A qaOmitInner `json:"a,omitzero"`
}

func TestQAOmitZeroAndOmitEmptyAreToldApart(t *testing.T) {
	// Reference: the two tags write different JSON for the same zero value.
	emptyJSON, err := json.Marshal(qaOmitEmpty{})
	if err != nil {
		t.Fatal(err)
	}
	zeroJSON, err := json.Marshal(qaOmitZero{})
	if err != nil {
		t.Fatal(err)
	}
	if string(emptyJSON) == string(zeroJSON) {
		t.Fatalf("the two tags write the same JSON for a zero value (%s), so this fixture does not exercise the difference", emptyJSON)
	}

	declared := loadDeclaredTypes(t)
	emptyFields := fields(declared.get(t, "qaOmitEmpty"))
	zeroFields := fields(declared.get(t, "qaOmitZero"))
	if !slices.Equal(emptyFields, zeroFields) {
		// Once the manifest distinguishes the options this test has nothing
		// left to prove: the wire change is visible and -base rejects it.
		return
	}

	old := &Manifest{Codecs: []Codec{{
		Sum:     "example.com/qaOmit.Event",
		Name:    "qaOmit.changed",
		Version: 1,
		Fields:  emptyFields,
	}}}
	now := &Manifest{Codecs: []Codec{{
		Sum:     "example.com/qaOmit.Event",
		Name:    "qaOmit.changed",
		Version: 1,
		Fields:  zeroFields,
	}}}
	if problems := Incompatible(old, now); len(problems) == 0 {
		t.Fatalf("fields() renders json:%q and json:%q identically as %q, so Incompatible reports no change even though encoding/json writes %s and %s",
			"a,omitempty", "a,omitzero", emptyFields, emptyJSON, zeroJSON)
	}
}
