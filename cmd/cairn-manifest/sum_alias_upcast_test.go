package main

import "testing"

// The upcaster's sum is rendered the same way the registration's is, so an
// alias has to resolve in both or upcastsToCodecs cannot pair them and the
// extractor reports an upcaster for a codec it has.
func TestAnUpcasterOnASumWrittenThroughAnAliasIsPaired(t *testing.T) {
	m, err := Extract("testdata/aliasupcast", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: got %+v, want the one registration declared in testdata/aliasupcast", m.Codecs)
	}
	c := m.Codecs[0]
	if got, want := c.Sum, "example.com/aliasupcast.Command"; got != want {
		t.Errorf("sum: got %q, want %q", got, want)
	}
	if got, want := c.Upcasts, []int{1}; len(got) != len(want) || got[0] != want[0] {
		t.Errorf("upcasts: got %v, want %v", got, want)
	}
}
