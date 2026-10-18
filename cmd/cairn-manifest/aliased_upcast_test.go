package main

import (
	"slices"
	"testing"
)

// The codec an upcaster is registered on can be named through an alias of a
// pointer to codec.Codec. go/types keeps the alias, so an isMethod that only
// looks for a *types.Pointer before resolving it does not see the method and
// the upcaster is dropped from the manifest without an error.
func TestExtractReadsAnUpcastRegisteredThroughAnAliasedCodec(t *testing.T) {
	m, err := Extract("testdata/aliasedupcast", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want the one registration declared in testdata/aliasedupcast", m.Codecs)
	}
	c := m.Codecs[0]
	if got, want := c.Upcasts, []int{1}; !slices.Equal(got, want) {
		t.Errorf("upcasts: got %v, want %v", got, want)
	}
}
