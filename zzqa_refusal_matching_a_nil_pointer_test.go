package cairn_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn"
)

// errors.Is is the documented way to match a refusal, and a sentinel can be a
// nil *cairn.Refusal: a var left unset, or a typed nil returned from a helper
// that builds one. Matching must answer "no match" for it rather than
// dereference it, so the caller still gets the error it was checking.
func TestRefusalMatchingANilPointerDoesNotPanic(t *testing.T) {
	refusal := &cairn.Refusal{Code: "invalid"}
	sentinel := &cairn.Refusal{Code: "invalid"}

	if !errors.Is(refusal, sentinel) {
		t.Fatal("a refusal no longer matches a sentinel with the same code")
	}

	var nilTarget *cairn.Refusal
	if errors.Is(refusal, nilTarget) {
		t.Fatal("a refusal matched a nil sentinel")
	}

	var nilRefusal *cairn.Refusal
	if errors.Is(nilRefusal, sentinel) {
		t.Fatal("a nil refusal matched a sentinel")
	}
}
