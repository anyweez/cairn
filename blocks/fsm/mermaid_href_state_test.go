package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
)

// Mermaid's state-diagram lexer has a bare "href" token (used by the click
// statement) before it will read a word as a state id, and its options are
// case-insensitive. isMermaidID refuses "click" but not "href", so a lifecycle
// state called href is emitted as a bare id: `href --> open: go`. The lexer
// tokenizes that source as HREF, which is not valid where an id is expected,
// and the whole diagram is rejected rather than just that edge.
func TestMermaidStateCannotBeHref(t *testing.T) {
	for _, word := range []string{"href", "HREF", "Href"} {
		table := fsm.New(
			fsm.Edge[string, string]{From: word, On: "go", To: "open"},
			fsm.Edge[string, string]{From: "open", On: "close", To: "closed"},
		)
		for _, line := range strings.Split(table.Mermaid(), "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, "-->") {
				continue
			}
			from, _, _ := strings.Cut(line, "-->")
			if strings.EqualFold(strings.TrimSpace(from), word) {
				t.Fatalf("edge %q starts with the Mermaid token %q, which the state-diagram lexer reads as a click directive, so the diagram does not parse", line, strings.TrimSpace(from))
			}
		}
	}
}
