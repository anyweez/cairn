package fsm_test

import (
	"strings"
	"testing"

	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/spec"
)

// Law turns a table into a spec change: a stream moves only along an edge, on
// the trigger that edge names. This checks the three answers directly, since
// the spec runner is not in the loop here.
func TestLawChecksAChangeAgainstTheTable(t *testing.T) {
	// A fixture rule, declared in the function so the manifest tool does not
	// read it as a rule of the library.
	rule := spec.Rule{
		ID: "T1", Kind: spec.Never, Status: spec.Established,
		Statement: "a ticket leaves open without being closed.",
	}
	table := fsm.New(
		fsm.Edge[string, string]{From: "open", On: "close", To: "closed"},
		fsm.Edge[string, string]{From: "closed", On: "reopen", To: "open"},
	)
	type state struct{ Status string }
	type message struct{ Trigger string }
	law := fsm.Law(rule, table,
		func(s state) string { return s.Status },
		func(m message) string { return m.Trigger })

	pass := law.Check(state{"open"}, message{"close"}, state{"closed"})
	if !pass.Applies() {
		t.Error("an edge the table has must apply")
	}
	if detail, failed := pass.Failed(); failed {
		t.Errorf("an edge the table has failed: %s", detail)
	}

	na := law.Check(state{"open"}, message{"close"}, state{"open"})
	if na.Applies() {
		t.Error("a change that leaves the state alone must not apply")
	}

	bad := law.Check(state{"closed"}, message{"close"}, state{"open"})
	detail, failed := bad.Failed()
	if !failed || !strings.Contains(detail, "does not allow") {
		t.Errorf("a move the table lacks: failed %v, detail %q", failed, detail)
	}
}
