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
