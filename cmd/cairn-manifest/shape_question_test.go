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

// fields() marks a field with omitted values using "?" after its name
// ("?zero" for omitzero, "?empty,zero" for both), and "?" is also a legal
// character in a JSON key. The manifest therefore renders a field named "a?"
// and a field named "a" tagged omitempty identically, so the -base check
// cannot see a change from one to the other and passes a wire-format break.
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

// bothOptions carries both omission rules: encoding/json then omits a value
// when either one says so, which is a third wire format and must render
// differently from either option on its own.
type bothOptions struct {
	A int `json:"a,omitempty,omitzero"`
}

func TestBothOmissionOptionsHaveTheirOwnMarker(t *testing.T) {
	if got, _ := json.Marshal(bothOptions{}); string(got) != `{}` {
		t.Fatalf("bothOptions marshaled to %s, want {}", got)
	}
	declared := loadDeclaredTypes(t)
	empty := fields(declared.get(t, "omittedKey"))
	zero := fields(declared.get(t, "questionZeroKey"))
	both := fields(declared.get(t, "bothOptions"))
	if slices.Equal(empty, both) || slices.Equal(zero, both) {
		t.Fatalf("fields() renders both options as %v, the same as one option alone: empty %v, zero %v", both, empty, zero)
	}
}
