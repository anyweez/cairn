package main

// encoding/json resolves a field name reached through more than one embedded
// struct by depth: the shallowest wins, and a tie drops both. The manifest's
// field shaper does not, so it lists fields that are never written to JSON.
//
// The type below is the simplest case: the outer Name shadows the one promoted
// from dominanceInner. encoding/json writes one "name" key; fields() lists two.

import (
	"encoding/json"
	"go/types"
	"testing"

	"golang.org/x/tools/go/packages"
)

type dominanceInner struct {
	Name string `json:"name"`
}

type dominanceOuter struct {
	dominanceInner
	Name string `json:"name"`
}

func TestAShadowedEmbeddedFieldIsNotAStoredField(t *testing.T) {
	cfg := &packages.Config{
		Mode:  packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo,
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, ".")
	if err != nil {
		t.Fatal(err)
	}
	var typ types.Type
	for _, p := range pkgs {
		if obj := p.Types.Scope().Lookup("dominanceOuter"); obj != nil {
			typ = obj.Type()
		}
	}
	if typ == nil {
		t.Fatal("dominanceOuter was not found in the loaded packages")
	}

	// What encoding/json actually writes: the outer field wins, the embedded
	// one is shadowed, so there is one key.
	raw, err := json.Marshal(dominanceOuter{dominanceInner: dominanceInner{Name: "inner"}, Name: "outer"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}

	got := fields(typ)
	if len(got) != len(wire) {
		t.Fatalf("manifest lists %d fields %v, but encoding/json writes %d key(s) %s", len(got), got, len(wire), raw)
	}
}
