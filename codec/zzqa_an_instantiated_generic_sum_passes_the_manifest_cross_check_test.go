package codec_test

// The two halves of the spec manifest name a sum type the same way: the
// extractor records `types.TypeString` of the type argument written at the
// `codec.Register` call, and `Registrations` names it with `goTypeString`,
// whose doc comment promises "a runtime type as go/types does for the
// manifest" because "a sum type the two name differently fails
// codec.CheckRegistrations in both directions for one correct codec".
//
// For an instantiated generic type reflect's `Name()` joins the type arguments
// with "," where go/types writes ", ". So a sum that takes two or more type
// arguments — a generic event or message sum keyed by what it names and what it
// carries — is named differently by the two halves, and the documented
// cross-check reports one correct codec as both missing from the manifest and
// extra in the codecs. A user cannot make that pass: the manifest is generated
// from the same source, so both sides are correct and disagree only on
// spelling.
//
// The reproduction pins go/types' spelling, which is what `cairn-manifest`
// writes for this type, and then asks the cross-check that the README documents
// and `examples/decisions/manifest_test.go` demonstrates to accept it.

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/codec"
)

// zzqaPair is a sum parameterised by the key it names and what it carries,
// which is the shape a generic event or message sum has.
type zzqaPair[A any, B any] interface {
	isZZQAPair()
}

type zzqaKey struct {
	K string `json:"k"`
}

type zzqaNoted struct {
	Note string `json:"note"`
}

func (zzqaNoted) isZZQAPair() {}

func TestAnInstantiatedGenericSumNamesTheSameWayAsTheManifest(t *testing.T) {
	c := codec.New[zzqaPair[zzqaKey, string]]()
	codec.Register[zzqaNoted, zzqaPair[zzqaKey, string]](c, "zzqa.generic", 1)

	// What the manifest records for this registration: types.TypeString of the
	// sum, which separates the type arguments with ", ".
	const manifestSum = "github.com/anyweez/cairn/codec_test.zzqaPair[github.com/anyweez/cairn/codec_test.zzqaKey, string]"

	registrations := c.Registrations()
	if got := registrations[0].Sum; got != manifestSum {
		t.Errorf("the codecs name the sum %q, the manifest records %q", got, manifestSum)
	}
	if problems := codec.CheckRegistrations([]codec.Registration{{Sum: manifestSum, Name: "zzqa.generic", Version: 1}}, registrations); len(problems) > 0 {
		t.Errorf("the documented cross-check reports one correct codec as two:\n  %s", strings.Join(problems, "\n  "))
	}
}
