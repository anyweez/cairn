package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
)

// Mermaid reads an edge as "From --> To: label", and its state-diagram lexer
// matches the label with `\s*:[^:\n;]+`: the text after the separator cannot
// contain another colon. The only lifecycle in the repository names every
// status change "set_status:<status>", so its rendered diagram is rejected by
// the parser as a whole (mermaid 11 reports "Parse error ... got 'DESCR'"),
// and the promised review rendering produces nothing.
//
// A label's own colon therefore has to be escaped like the other syntax
// characters `mermaidLabel` handles, e.g. as #58;, which the renderer decodes
// back to ":" in the label.
func TestMermaidTriggerLabelCannotContainTheSeparator(t *testing.T) {
	table := fsm.New(
		fsm.Edge[string, string]{From: "open", On: "record_outcome", To: "decided"},
		fsm.Edge[string, string]{From: "open", On: "set_status:deferred", To: "deferred"},
	)
	for _, line := range strings.Split(table.Mermaid(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "-->") {
			continue
		}
		// Exactly one colon: the separator this method writes. Any other is
		// part of the trigger and would end the label early.
		if got := strings.Count(line, ":"); got != 1 {
			t.Fatalf("edge %q has %d colons; Mermaid ends the label at the first, so a trigger that contains one makes the whole diagram unparseable", line, got)
		}
	}
}
