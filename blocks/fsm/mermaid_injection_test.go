package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
)

// A trigger is a label, not diagram syntax: it must not be able to introduce a
// transition the table does not contain. Mermaid renders an edge as
// "From --> To: On", so an On value that itself contains "-->" is read by the
// parser as another edge, and the rendered diagram shows a state the table
// never declared. The injected name does not exist in Go, so the review
// diagram lies about the lifecycle.
func TestMermaidTriggerCannotInjectAnEdge(t *testing.T) {
	table := fsm.New(
		fsm.Edge[string, string]{From: "open", On: "close", To: "closed"},
		fsm.Edge[string, string]{From: "closed", On: "reopen", To: "open"},
	)
	// The table has two edges. A diagram of it must show two transitions, no
	// matter what a trigger is called.
	if got, want := strings.Count(table.Mermaid(), "-->"), len(table.Edges()); got != want {
		t.Fatalf("Mermaid shows %d transitions for %d edges:\n%s", got, want, table.Mermaid())
	}

	injected := fsm.New(
		fsm.Edge[string, string]{From: "open", On: "close-->archived", To: "closed"},
	)
	out := injected.Mermaid()
	if strings.Contains(out, "archived") {
		t.Fatalf("a trigger introduced the state %q into the diagram:\n%s", "archived", out)
	}
}
