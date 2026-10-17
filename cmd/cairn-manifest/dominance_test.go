package main

// encoding/json resolves a field name reached through more than one embedded
// struct by depth: the shallowest wins, and a tie drops both. The manifest's
// field shaper does not, so it lists fields that are never written to JSON.
//
// The type below is the simplest case: the outer Name shadows the one promoted
// from dominanceInner. encoding/json writes one "name" key; fields() lists two.

import (
	"encoding/json"
	"go/token"
	"go/types"
	"slices"
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

// Two more of encoding/json's dominance rules. A name reached at the same
// depth from two fields is ambiguous and dropped, unless exactly one of them
// is tagged, in which case the tagged one wins. The types are built with
// go/types rather than declared in source, because two fields repeating a
// JSON tag is itself a vet error; encoding/json writes {} for the tied shape
// and {"Name":0} for the tagged one.
func TestFieldDominanceDropsATieAndPrefersATag(t *testing.T) {
	withField := func(name string, typ types.Type, tag string) *types.Struct {
		return types.NewStruct(
			[]*types.Var{types.NewField(token.NoPos, nil, name, typ, false)},
			[]string{tag},
		)
	}
	embedding := func(a, b *types.Struct) *types.Struct {
		return types.NewStruct(
			[]*types.Var{
				types.NewField(token.NoPos, nil, "A", a, true),
				types.NewField(token.NoPos, nil, "B", b, true),
			},
			[]string{"", ""},
		)
	}

	for _, tc := range []struct {
		name string
		typ  types.Type
		want []string
	}{
		{
			// Same JSON name at the same depth, neither tagged: the name is
			// ambiguous, so encoding/json writes no key for it.
			"tie",
			embedding(withField("Name", types.Typ[types.String], `json:"name"`),
				withField("Name", types.Typ[types.String], `json:"name"`)),
			nil,
		},
		{
			// Same JSON name at the same depth, one tagged: the tagged field
			// wins, and its int shape is what the manifest must record.
			"tag wins",
			embedding(withField("Name", types.Typ[types.String], ""),
				withField("Name", types.Typ[types.Int], `json:"Name"`)),
			[]string{"Name: int"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := fields(tc.typ); !slices.Equal(got, tc.want) {
				t.Fatalf("fields() = %v, want %v", got, tc.want)
			}
		})
	}
}
