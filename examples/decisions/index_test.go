package decisions

import "testing"

// The index holds the refs of the newest entry it has applied. A delivery
// older than the refs it holds is a retry the outbox delivered out of order;
// applying it would answer with text the decision no longer has.
func TestReferenceIndexIgnoresAnOlderDelivery(t *testing.T) {
	content := NewContent()
	index := NewReferenceIndex(content)
	first := content.Put("first")
	second := content.Put("second")
	third := content.Put("third")

	index.Apply(2, IndexReferences{Decision: "d-1", Title: second})
	index.Apply(1, IndexReferences{Decision: "d-1", Title: first})
	if got := index.Text("d-1"); got != "second" {
		t.Fatalf("the index holds %q after a stale delivery, want %q", got, "second")
	}

	// A newer delivery still applies, and the same entry may be delivered
	// again without moving the index.
	index.Apply(3, IndexReferences{Decision: "d-1", Title: third})
	index.Apply(3, IndexReferences{Decision: "d-1", Title: third})
	if got := index.Text("d-1"); got != "third" {
		t.Fatalf("the index holds %q after a newer delivery, want %q", got, "third")
	}

	// One decision's seq does not order another's.
	if got := index.Text("d-2"); got != "" {
		t.Fatalf("the index holds %q for an unseen decision, want empty", got)
	}
}

// The index keeps the refs a decision's text lives under, not the text, so
// deleting a row for a deletion request takes the words out of the index too.
// A reader gets no text for the row, not the ref that named it.
func TestReferenceIndexForgetsADeletedRow(t *testing.T) {
	content := NewContent()
	index := NewReferenceIndex(content)
	title := content.Put("Should we ship the beta?")
	rationale := content.Put("The bugs are cosmetic")
	index.Apply(1, IndexReferences{Decision: "d-1", Title: title, Rationale: rationale})

	content.Delete(title, rationale)

	if got := index.Text("d-1"); got != "" {
		t.Fatalf("the index still answers with %q after the rows were deleted", got)
	}
}
