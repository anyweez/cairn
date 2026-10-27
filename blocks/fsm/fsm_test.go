package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
)

func TestTable(t *testing.T) {
	table := fsm.New(
		fsm.Edge[string, string]{From: "open", On: "close", To: "closed"},
		fsm.Edge[string, string]{From: "closed", On: "reopen", To: "open"},
	)
	if to, ok := table.Next("open", "close"); !ok || to != "closed" {
		t.Fatalf("Next = %q, %v", to, ok)
	}
	if table.Allows("closed", "close", "closed") {
		t.Fatal("no edge leaves closed on close")
	}
	want := "stateDiagram-v2\n    closed --> open: reopen\n    open --> closed: close\n"
	if got := table.Mermaid(); got != want {
		t.Fatalf("Mermaid:\n%s", got)
	}
}

// A lifecycle's initial state is often the zero value. Mermaid needs a
// non-empty source, so an empty one makes the whole diagram unparseable.
func TestMermaidNamesASourceForEveryEdge(t *testing.T) {
	table := fsm.New(
		fsm.Edge[string, string]{From: "", On: "create", To: "open"},
		fsm.Edge[string, string]{From: "open", On: "close", To: "closed"},
	)
	for _, line := range strings.Split(table.Mermaid(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "-->") {
			continue
		}
		from, _, _ := strings.Cut(line, "-->")
		if strings.TrimSpace(from) == "" {
			t.Fatalf("edge line %q has no source state, so Mermaid rejects the diagram", line)
		}
	}
}

// Mermaid's entity syntax is "#NN;" without a leading ampersand. Emitting
// "&#NN;" leaves that ampersand in the rendered text instead of producing the
// intended character, so a trigger like "set_status:open" is displayed as
// "set_status&:open". These labels are the characters mermaidLabel escapes,
// with the exact diagram text expected for each; asserting on shape alone (a
// colon count, no "-->") cannot see the stray ampersand.
func TestMermaidLabelsAreEscapedWithoutAStrayAmpersand(t *testing.T) {
	for _, tc := range []struct {
		on   string
		want string
	}{
		{"set_status:open", "set_status#58;open"},
		{"semi;colon", "semi#59;colon"},
		{"amp&ersand", "amp#38;ersand"},
		{"a-->b", "#97;#45;#45;#62;#98;"},
	} {
		table := fsm.New(
			fsm.Edge[string, string]{From: "open", On: tc.on, To: "closed"},
		)
		want := "stateDiagram-v2\n    open --> closed: " + tc.want + "\n"
		if got := table.Mermaid(); got != want {
			t.Errorf("trigger %q:\ngot:\n%s\nwant:\n%s", tc.on, got, want)
		}
	}
}

// A state that is not a plain Mermaid id keeps its text as an alias label, and
// that label is escaped by the same function. The declaration has to use
// Mermaid's entity syntax too, or the rendered state name carries the stray
// ampersand.
func TestMermaidStateLabelIsEscapedWithoutAStrayAmpersand(t *testing.T) {
	table := fsm.New(
		fsm.Edge[string, string]{From: "open:closed", On: "go", To: "open"},
	)
	want := "stateDiagram-v2\n    state \"open#58;closed\" as s0\n    s0 --> open: go\n"
	if got := table.Mermaid(); got != want {
		t.Fatalf("Mermaid:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAmbiguousTablesPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("two edges from one state on one trigger must panic")
		}
	}()
	fsm.New(
		fsm.Edge[string, string]{From: "open", On: "close", To: "closed"},
		fsm.Edge[string, string]{From: "open", On: "close", To: "archived"},
	)
}
