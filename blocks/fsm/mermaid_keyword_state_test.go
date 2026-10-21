package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
)

// Mermaid's state-diagram lexer reads several words at the start of a line as
// directives before it will read them as a state id: `style` starts a styling
// statement, `default` starts a class default, and `stateDiagram` starts a
// document. A lifecycle whose state is one of those words renders an edge that
// the parser rejects, so the whole diagram is lost rather than showing the
// lifecycle.
//
// isMermaidID already refuses "state", "class", "classDef", "direction",
// "scale" and "hide"; these three are the rest of the words the lexer reads as
// syntax. Any fix works — an alias (`state "style" as s0`) is what other
// non-plain ids get — as long as the edge line does not begin with the bare
// keyword.
func TestMermaidStateCannotBeAMermaidKeyword(t *testing.T) {
	reserved := []string{
		"default",
		"style",
		"stateDiagram",
		// The ones the lexer also reserves, kept here so the test fails if the
		// guard is widened to catch these but later narrowed again.
		"state",
		"class",
		"classdef",
		"direction",
		"scale",
		"hide",
	}
	for _, word := range reserved {
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
				t.Fatalf("edge %q starts with the Mermaid keyword %q, which the state-diagram lexer reads as a directive, so the diagram does not parse", line, strings.TrimSpace(from))
			}
		}
	}
}
