package decisions

import "testing"

// The index holds the text of the newest entry it has applied. A delivery
// older than the text it holds is a retry the outbox delivered out of order;
// applying it would answer with text the decision no longer has.
func TestReferenceIndexIgnoresAnOlderDelivery(t *testing.T) {
	index := NewReferenceIndex()
	index.Apply(2, IndexReferences{Decision: "d-1", Text: "second"})
	index.Apply(1, IndexReferences{Decision: "d-1", Text: "first"})
	if got := index.Text("d-1"); got != "second" {
		t.Fatalf("the index holds %q after a stale delivery, want %q", got, "second")
	}

	// A newer delivery still applies, and the same entry may be delivered
	// again without moving the index.
	index.Apply(3, IndexReferences{Decision: "d-1", Text: "third"})
	index.Apply(3, IndexReferences{Decision: "d-1", Text: "third"})
	if got := index.Text("d-1"); got != "third" {
		t.Fatalf("the index holds %q after a newer delivery, want %q", got, "third")
	}

	// One decision's seq does not order another's.
	if got := index.Text("d-2"); got != "" {
		t.Fatalf("the index holds %q for an unseen decision, want empty", got)
	}
}
