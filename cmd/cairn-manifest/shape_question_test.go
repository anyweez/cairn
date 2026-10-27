package main

import (
	"encoding/json"
	"slices"
	"testing"
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

	declared := loadDeclaredTypes(t)
	q := declared.get(t, "questionKey")
	o := declared.get(t, "omittedKey")
	if slices.Equal(fields(q), fields(o)) {
		t.Fatalf("the manifest renders json:%q and json:%q the same: %v, so -base cannot tell the two wire formats apart",
			"a?", "a,omitempty", fields(q))
	}
}

// questionZeroKey's field is omitted when zero, like questionKey but with
// omitzero, so the same collision applies to that marker.
type questionZeroKey struct {
	A int `json:"a,omitzero"`
}

// nestedQuestionKey and nestedOmittedKey put the collision one level down:
// the old rendering of both was chain: {next?: int}.
type nestedQuestionKey struct {
	Chain struct {
		Next int `json:"next?"`
	} `json:"chain"`
}

type nestedOmittedKey struct {
	Chain struct {
		Next int `json:"next,omitempty"`
	} `json:"chain"`
}

func TestAQuestionMarkIsNotTheOmitzeroMarkerEither(t *testing.T) {
	if got, _ := json.Marshal(questionZeroKey{}); string(got) != `{}` {
		t.Fatalf("questionZeroKey marshaled to %s, want {}", got)
	}
	declared := loadDeclaredTypes(t)
	q := declared.get(t, "questionKey")
	z := declared.get(t, "questionZeroKey")
	if slices.Equal(fields(q), fields(z)) {
		t.Fatalf("the manifest renders json:%q and json:%q the same: %v", "a?", "a,omitzero", fields(q))
	}
}

func TestAQuestionMarkInANestedKeyIsNotTheOmitemptyMarker(t *testing.T) {
	if got, _ := json.Marshal(nestedQuestionKey{}); string(got) != `{"chain":{"next?":0}}` {
		t.Fatalf("nestedQuestionKey marshaled to %s, want {\"chain\":{\"next?\":0}}", got)
	}
	if got, _ := json.Marshal(nestedOmittedKey{}); string(got) != `{"chain":{}}` {
		t.Fatalf("nestedOmittedKey marshaled to %s, want {\"chain\":{}}", got)
	}
	declared := loadDeclaredTypes(t)
	q := declared.get(t, "nestedQuestionKey")
	o := declared.get(t, "nestedOmittedKey")
	if slices.Equal(fields(q), fields(o)) {
		t.Fatalf("the manifest renders the nested json:%q and json:%q the same: %v", "next?", "next,omitempty", fields(q))
	}
}
