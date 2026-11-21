package main

import (
	"reflect"
	"strings"
	"testing"
)

type zzArgKey struct {
	K string `json:"k"`
}

// zzArgAlias is an alias for zzArgKey, not a type of its own.
type zzArgAlias = zzArgKey

type zzArgEvent[A any, B any] interface{ isZZArgEvent() }

// reflect resolves an alias inside a composite type argument as it does a bare
// one, so the runtime name of a sum instantiated with []Alias and *Alias names
// the aliased type twice. The extractor has to resolve both for the manifest
// cross-check to pass.
func TestAnAliasInsideACompositeTypeArgumentIsNamedAsTheRuntimeNamesIt(t *testing.T) {
	if got := reflect.TypeFor[zzArgEvent[[]zzArgAlias, *zzArgAlias]]().Name(); strings.Contains(got, "zzArgAlias") || !strings.Contains(got, "[]github.com/anyweez/cairn/cmd/cairn-manifest.zzArgKey") {
		t.Fatalf("reflect names a composite holding an aliased type argument %q, so the runtime sum carries the alias", got)
	}

	m, err := Extract("testdata/aliassumcomposite", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want the one registration in testdata/aliassumcomposite", m.Codecs)
	}
	want := "example.com/aliassumcomposite.Event[[]example.com/aliassumcomposite.Key, *example.com/aliassumcomposite.Key]"
	if got := m.Codecs[0].Sum; got != want {
		t.Fatalf("sum: the manifest records %q, but the runtime registration for the same declaration is %q", got, want)
	}
}
