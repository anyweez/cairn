package main

import (
	"encoding/json"
	"go/types"
	"slices"
	"testing"

	"golang.org/x/tools/go/packages"
)

// questionKey names its JSON key with a trailing "?", which encoding/json
// accepts: the field is always written as "a?".
type questionKey struct {
	A int `json:"a?"`
}

// omittedKey is the same field with omitempty: its key is "a", and it is left
// out when the value is zero. The two write different JSON.
type omittedKey struct {
	A int `json:"a,omitempty"`
}

// fields() marks an omitempty (or omitzero) field by appending "?" to its
// name, which is also a legal character in a JSON key. The manifest therefore
// renders these two wire formats identically, so the -base check cannot see a
// change from one to the other and passes a wire-format break.
func TestAFieldNamedWithATrailingQuestionMarkIsNotTheOmitemptyMarker(t *testing.T) {
	if got, _ := json.Marshal(questionKey{}); string(got) != `{"a?":0}` {
		t.Fatalf("questionKey marshaled to %s, want {\"a?\":0}", got)
	}
	if got, _ := json.Marshal(omittedKey{}); string(got) != `{}` {
		t.Fatalf("omittedKey marshaled to %s, want {}", got)
	}

	q := declaredType(t, "questionKey")
	o := declaredType(t, "omittedKey")
	if slices.Equal(fields(q), fields(o)) {
		t.Fatalf("the manifest renders json:%q and json:%q the same: %v, so -base cannot tell the two wire formats apart",
			"a?", "a,omitempty", fields(q))
	}
}

// declaredType loads the named type from this package, the way the other
// shape tests reach the real declaration.
func declaredType(t *testing.T, name string) types.Type {
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
