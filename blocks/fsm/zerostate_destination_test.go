package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
)

// Mermaid has no empty state id. A lifecycle that renders an edge into the
// zero-value state — an entity deleted, an order cancelled back to nothing —
// writes an edge with no destination, and Mermaid rejects the whole diagram,
// not just that line.
func TestMermaidNamesADestinationForEveryEdge(t *testing.T) {
	table := fsm.New(
		fsm.Edge[string, string]{From: "open", On: "delete", To: ""},
	)
	for _, line := range strings.Split(table.Mermaid(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "stateDiagram-v2" {
			continue
		}
		if !strings.Contains(line, "-->") {
			t.Fatalf("not an edge: %q", line)
		}
		_, dest, _ := strings.Cut(line, "-->")
		dest, _, _ = strings.Cut(dest, ":")
		if strings.TrimSpace(dest) == "" {
			t.Fatalf("edge %q names no destination state; Mermaid has no empty state id, so the diagram does not parse", line)
		}
	}
}
