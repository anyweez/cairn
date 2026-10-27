package decisions_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/examples/decisions"
)

// Lifecycle is rendered for review, so its diagram has to parse. Every status
// change is named "set_status:<status>", and Mermaid ends an edge's label at
// the separator, so an unescaped colon in a trigger loses the whole diagram.
func TestLifecycleRendersAsAMermaidDiagram(t *testing.T) {
	out := decisions.Lifecycle.Mermaid()
	var edges int
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "stateDiagram-v2" || strings.HasPrefix(line, "state ") {
			continue
		}
		edges++
		from, rest, ok := strings.Cut(line, "-->")
		if !ok || strings.TrimSpace(from) == "" {
			t.Fatalf("edge %q has no source state:\n%s", line, out)
		}
		if dest, _, _ := strings.Cut(rest, ":"); strings.TrimSpace(dest) == "" {
			t.Fatalf("edge %q has no destination state:\n%s", line, out)
		}
		// One colon: the separator Mermaid writes. A second is part of the
		// trigger and ends the label early, which rejects the whole diagram.
		if got := strings.Count(line, ":"); got != 1 {
			t.Fatalf("edge %q has %d colons, which Mermaid rejects:\n%s", line, got, out)
		}
	}
	if want := len(decisions.Lifecycle.Edges()); edges != want {
		t.Fatalf("diagram shows %d edges, lifecycle has %d:\n%s", edges, want, out)
	}
}
