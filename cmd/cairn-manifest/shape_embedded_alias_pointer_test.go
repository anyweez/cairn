package main

import (
	"bytes"
	"encoding/json"
	"go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

// An embedded unexported alias of a pointer to a struct is legal Go, and
// encoding/json marshals it as a named object when the field has a JSON tag.
// The manifest's shaper resolves aliases when it flattens an embedded field,
// but its unexported-embedded guard tests for *types.Pointer without resolving
// the alias first, so the field is dropped from the stored shape.
//
// The consequence is the one t2553 describes: two versions whose nested object
// changed produce the same manifest, so Incompatible reports nothing and a
// version bump is never forced.
type aliasPointerMetaV1 struct {
	Old string `json:"old"`
}

type aliasPointerMetaV2 struct {
	New string `json:"new"`
}

type aliasPointerV1 = *aliasPointerMetaV1
type aliasPointerV2 = *aliasPointerMetaV2

type aliasPointerOuterV1 struct {
	aliasPointerV1 `json:"meta"`
	Y              int `json:"y"`
}

type aliasPointerOuterV2 struct {
	aliasPointerV2 `json:"meta"`
	Y              int `json:"y"`
}

// loadedTypeFor returns the declared type of the package-level type with this
// name, as the extractor sees it.
func loadedTypeFor(t *testing.T, name string) types.Type {
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

func TestAnEmbeddedUnexportedPointerAliasKeepsItsTaggedShape(t *testing.T) {
	// What encoding/json stores for the declaration.
	raw, err := json.Marshal(aliasPointerOuterV1{aliasPointerV1: &aliasPointerMetaV1{Old: "o"}, Y: 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"meta":{"old":"o"},"y":1}`; string(raw) != want {
		t.Fatalf("reference: encoding/json wrote %s, want %s", raw, want)
	}

	// What the manifest says is stored.
	got := fields(loadedTypeFor(t, "aliasPointerOuterV1"))
	if !slices.ContainsFunc(got, func(f string) bool { return len(f) >= 6 && f[:6] == "meta: " }) {
		t.Fatalf("fields() = %q, but encoding/json stores %s; the embedded tagged field is missing from the manifest", got, raw)
	}

	// The purpose of the shape: the -base check must see the nested change.
	oldShape := fields(loadedTypeFor(t, "aliasPointerOuterV1"))
	newShape := fields(loadedTypeFor(t, "aliasPointerOuterV2"))
	old := &Manifest{Codecs: []Codec{{Sum: "example.com/x.Event", Name: "x.event", Version: 1, Fields: oldShape}}}
	now := &Manifest{Codecs: []Codec{{Sum: "example.com/x.Event", Name: "x.event", Version: 1, Fields: newShape}}}
	if problems := Incompatible(old, now); len(problems) == 0 {
		t.Fatalf("-base reports nothing for a stored shape that changed from %s to %s; the manifest listed %q either way",
			`{"meta":{"old":""},"y":0}`, `{"meta":{"new":""},"y":0}`, oldShape)
	}

	// Why that matters: an entry written by the old shape no longer decodes.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var into aliasPointerOuterV2
	if err := dec.Decode(&into); err == nil {
		t.Fatalf("the old entry %s decoded into the new type, so the change is not a wire break", raw)
	}
}
