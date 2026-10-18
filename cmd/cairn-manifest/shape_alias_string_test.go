package main

import (
	"encoding/json"
	"go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

// aliasedIntPtr is an alias of *int. reflect erases aliases, so encoding/json
// sees a plain *int and honours the ",string" option: the value is written
// inside a JSON string.
type aliasedIntPtr = *int

type aliasStringField struct {
	S aliasedIntPtr `json:"s,string"`
}

type directStringField struct {
	S *int `json:"s,string"`
}

// quotable follows a pointer to decide whether the ",string" option applies,
// but it tests for a *types.Pointer before resolving a type alias. An alias of
// *int is a *types.Alias whose underlying type is a pointer, so the option is
// missed and the manifest renders the field as `*int` rather than
// `string(*int)`. encoding/json quotes both, so the manifest disagrees with
// what is stored — and with itself, since the same field written through the
// alias and through the pointer is one wire format.
func TestAStringOptionOnAnAliasedPointerIsRenderedLikeThePointerItNames(t *testing.T) {
	x := 7
	viaAlias, err := json.Marshal(aliasStringField{S: &x})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := json.Marshal(directStringField{S: &x})
	if err != nil {
		t.Fatal(err)
	}
	if string(viaAlias) != string(direct) {
		t.Fatalf("the two declarations write different JSON, so they are not comparable: %s vs %s", viaAlias, direct)
	}

	af := fields(declaredAliasType(t, "aliasStringField"))
	df := fields(declaredAliasType(t, "directStringField"))
	if !slices.Equal(af, df) {
		t.Fatalf("both write %s, but the manifest renders the alias as %v and the pointer as %v", viaAlias, af, df)
	}
}

// declaredAliasType loads the named type from this package, the way the other
// shape tests reach the real declaration.
func declaredAliasType(t *testing.T, name string) types.Type {
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
