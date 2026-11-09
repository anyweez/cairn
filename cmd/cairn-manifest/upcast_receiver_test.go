package main

import (
	"slices"
	"strings"
	"testing"
)

// A method expression passes the codec as the call's first argument. The
// method's declared receiver still names the codec, so the upcaster belongs in
// the manifest rather than being dropped because the call has no receiver.
func TestExtractReadsAnUpcastCalledAsAMethodExpression(t *testing.T) {
	m, err := Extract("testdata/methodupcast", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	if got, want := m.Codecs[0].Upcasts, []int{1}; !slices.Equal(got, want) {
		t.Errorf("upcasts: got %v, want %v", got, want)
	}
}

// A wrapper that declares its own Upcast hides the embedded codec's method. The
// extractor cannot tell which codec, if any, the call registers on, so it
// refuses the call instead of silently recording nothing.
func TestExtractRefusesAnUpcastItCannotAttribute(t *testing.T) {
	_, err := Extract("testdata/shadowupcast", "./...")
	if err == nil {
		t.Fatal("an Upcast call that hides the codec's method must be refused")
	}
	if want := "shadowupcast.go:26: Upcast: *example.com/shadowupcast.Events declares its own Upcast, so the codec it registers on cannot be read; call Upcast on the codec itself"; !strings.Contains(err.Error(), want) {
		t.Errorf("missing %q in:\n%v", want, err)
	}
}
