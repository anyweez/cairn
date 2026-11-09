package fsm_test

import (
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/anyweez/cairn/blocks/fsm"
)

// An aliased state is declared as `state "label" as s0`, and Mermaid shows
// that label — decoded from its #NN; entities back to characters — as the
// state's name. So the label has to be the state's text, or the review
// diagram names a state the table does not declare. Mermaid takes the text
// between the quotes as it stands; a Go-quoted form written into the diagram
// is shown literally, backslashes and all.
func TestMermaidStateLabelIsTheStatesText(t *testing.T) {
	for _, state := range []string{
		"open:closed", // a colon is escaped to #58;, which Mermaid decodes back
		"a\u00a0b",    // a non-breaking space, pasted in from a document
		"a\u200bb",    // a zero-width space
		"a\u00adb",    // a soft hyphen
		"a\ufeffb",    // a byte-order mark
		"a\x00b",      // a control character
		"a\x7fb",
	} {
		out := fsm.New(fsm.Edge[string, string]{From: state, On: "go", To: "open"}).Mermaid()
		if got := mermaidDeclaredLabel(t, out); got != state {
			t.Fatalf("the diagram names state %q as %q: Mermaid shows the declaration's label as the state's name, so the diagram does not show the table the code declares:\n%s",
				state, got, out)
		}
	}
}

// mermaidDeclaredLabel returns the label of the table's state declaration as
// Mermaid reads it: the text between the quotes, with #NN; decoded back to
// characters. It deliberately does not Go-unquote the text, because Mermaid
// does not: a backslash in the diagram is a backslash.
func mermaidDeclaredLabel(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "state ") {
			continue
		}
		if _, rest, ok := strings.Cut(line, `"`); ok {
			label, _, ok := strings.Cut(rest, `"`)
			if !ok {
				t.Fatalf("declaration %q has no closing quote", line)
			}
			return decodeMermaidEntities(label)
		}
		t.Fatalf("declaration %q is not quoted", line)
	}
	t.Fatalf("no state declaration in the diagram:\n%s", out)
	return ""
}

// decodeMermaidEntities turns Mermaid's #NN; entities into the characters they
// stand for, which is what the renderer shows.
func decodeMermaidEntities(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '#' {
			if j := strings.IndexByte(s[i:], ';'); j > 1 {
				if n, err := strconv.Atoi(s[i+1 : i+j]); err == nil {
					b.WriteRune(rune(n))
					i += j + 1
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		i += size
	}
	return b.String()
}
