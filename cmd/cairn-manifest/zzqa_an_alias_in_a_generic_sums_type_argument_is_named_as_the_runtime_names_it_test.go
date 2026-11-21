package main

// A generic sum may be instantiated with a type alias. Go resolves the alias,
// so the runtime type of Event[AliasKey, string] is Event[Key, string], and
// reflect — which codec.Registration.Sum is built from — has no aliases and
// names the aliased type. The extractor renders a type argument with go/types,
// which keeps the alias, so the manifest records a sum the running program has
// never heard of. codec.CheckRegistrations, the cross-check the README tells a
// module to write, then reports one correct codec as both missing from the
// manifest and extra in the codecs.
//
// This is the alias rule of t2685 applied one level down: that fix unaliases
// the sum itself (types.Unalias(sum)); it does not reach an alias inside the
// sum's type-argument list.

import (
	"reflect"
	"strings"
	"testing"
)

type zzqaArgKey struct {
	K string `json:"k"`
}

// zzqaArgAlias is an alias for zzqaArgKey, not a type of its own.
type zzqaArgAlias = zzqaArgKey

type zzqaArgEvent[A any, B any] interface{ isZZQAArgEvent() }

func TestAnAliasInAGenericSumsTypeArgumentIsNamedAsTheRuntimeNamesIt(t *testing.T) {
	// The runtime half of the declaration, in process: reflect is what
	// codec.Registration.Sum is computed with, and it resolves the alias, so
	// the sum it reports names zzqaArgKey and never zzqaArgAlias.
	if got := reflect.TypeFor[zzqaArgEvent[zzqaArgAlias, string]]().Name(); strings.Contains(got, "zzqaArgAlias") || !strings.Contains(got, "zzqaArgKey") {
		t.Fatalf("reflect names an aliased type argument %q, so the runtime sum carries the alias", got)
	}

	// The same declaration through the extractor: testdata/aliassumarg spells
	// the argument AliasKey, an alias for Key, so the manifest must record the
	// sum the runtime reports — Event[Key, string] — for the cross-check to
	// pass on a correct program.
	m, err := Extract("testdata/aliassumarg", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want the one registration in testdata/aliassumarg", m.Codecs)
	}
	want := "example.com/aliassumarg.Event[example.com/aliassumarg.Key, string]"
	if got := m.Codecs[0].Sum; got != want {
		t.Fatalf("sum: the manifest records %q, but a type alias is not a type: the runtime registration for the same declaration is %q", got, want)
	}
}
