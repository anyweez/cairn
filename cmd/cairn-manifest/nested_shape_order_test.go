package main

import "testing"

// Reordering a nested struct's fields changes nothing that is stored: JSON
// object keys are matched by name, so an entry already in a ledger decodes the
// same way whichever order the struct declares them in. The -base check is
// meant to compare shapes by field rather than by order for exactly that
// reason (see sameFields), but the order of a nested struct's fields is baked
// into the single shape string for its parent's field, so a harmless reorder
// is reported as "changed shape without a version bump" and fails the PR job.
//
// The base here is the manifest this module's types produced before Outcome's
// fields were reordered: the same JSON object with its keys in another order.
func TestReorderingANestedStructsFieldsIsNotAnIncompatibleChange(t *testing.T) {
	m, err := Extract("testdata/nestedorder", "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Codecs) != 1 {
		t.Fatalf("codecs: %+v", m.Codecs)
	}
	now := m.Codecs[0]
	if want := "outcome: {chosen: string, rationale: string, decided_by: string}"; len(now.Fields) != 1 || now.Fields[0] != want {
		t.Fatalf("fields: got %q, want [%q]", now.Fields, want)
	}

	base := &Manifest{Codecs: []Codec{{
		Sum:     now.Sum,
		Name:    now.Name,
		Version: now.Version,
		Fields:  []string{"outcome: {rationale: string, chosen: string, decided_by: string}"},
	}}}
	if problems := Incompatible(base, m); len(problems) > 0 {
		t.Fatalf("reordering the fields of a nested struct was reported as incompatible, so a change that cannot affect stored entries fails the base check: %v", problems)
	}
}
