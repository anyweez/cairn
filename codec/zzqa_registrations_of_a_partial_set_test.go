package codec_test

// A Set may carry only the codecs a machine has values for: spectest documents
// that "a nil codec in the set is not checked". The set is also what a program
// hands to the manifest cross-check, whose runtime half is
// Set.Registrations. A nil field has no registrations to report, so it must be
// skipped rather than dereferenced.

import (
	"reflect"
	"testing"

	"github.com/anyweez/cairn/codec"
)

type zzqaPartialMsg interface{ isZZQAPartial() }

type zzqaPartial struct{}

func (zzqaPartial) isZZQAPartial() {}

func TestRegistrationsOfAPartialSetSkipsNilCodecs(t *testing.T) {
	messages := codec.New[zzqaPartialMsg]()
	codec.Register[zzqaPartial](messages, "zzqa.partial", 1)
	set := &codec.Set[zzqaPartialMsg, struct{}, struct{}, struct{}]{Messages: messages}

	got := set.Registrations()
	if len(got) != 1 || got[0].Name != "zzqa.partial" {
		t.Fatalf("a set with only a messages codec reports %+v, want only zzqa.partial", got)
	}
	if got[0].Sum != "github.com/anyweez/cairn/codec_test.zzqaPartialMsg" {
		t.Fatalf("the sum is %q, want the messages codec's sum type", got[0].Sum)
	}
	if !reflect.DeepEqual(got, set.Registrations()) {
		t.Fatal("two calls report different registrations")
	}
}
