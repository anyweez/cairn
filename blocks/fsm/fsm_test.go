package fsm_test

import (
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
