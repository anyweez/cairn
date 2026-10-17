package main

import (
	"reflect"
	"testing"
)

// A Go alias is not a type: reflect resolves it, so the identity a program can
// report for a value of an aliased sum interface is the aliased type's name.
type command interface{ isCommand() }

type message = command

// codec.Registration.Sum is computed with reflect; the extractor's is computed
// with go/types, which keeps the alias. The manifest therefore records a sum the
// runtime has never heard of, and codec.CheckRegistrations — the runtime half of
// the cross-check the README tells a module to write — reports one codec as both
// missing and extra.
func TestASumWrittenThroughAnAliasIsNamedAsTheRuntimeNamesIt(t *testing.T) {
	if name := reflect.TypeFor[message]().Name(); name != "command" {
		t.Fatalf("reflect names an aliased sum %q, so a runtime registration carries the aliased type's name", name)
	}

	m, err := Extract("testdata/aliassum", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want the one registration declared in testdata/aliassum", m.Codecs)
	}
	if got, want := m.Codecs[0].Sum, "example.com/aliassum.Command"; got != want {
		t.Fatalf("sum: got %q, want %q, which is what the runtime registration reports for the same declaration", got, want)
	}
}
