package ledger_test

import (
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
)

// Verify's contract (README, "Replay verification") is that it re-decides
// every stored message with today's code and reports each divergence. A
// refusal is stored with no events (the schema's CHECK enforces that), and
// queueEntry refuses to record a decision that is refused and carries events
// or effects. So a Decider that now returns the same refusal code with events
// would not be able to record the entry it is replaying, yet Verify compares
// only the refusal codes once both decisions refuse, and reports nothing.
func TestVerifyReportsARefusalThatGainedEvents(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)

	// A refused message is recorded like any other; the counter's cap makes
	// this one over_cap with no events.
	if _, err := agg.Handle(ctx, "a", add{9, 4}); err == nil {
		t.Fatal("want an over_cap refusal")
	}

	// Today's code refuses with the same code, but now carries an event the
	// stored entry could not have: queueEntry rejects such a decision, so the
	// message would no longer be recordable at all.
	agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
		d := cairn.Refuse[int, event, effect]("over_cap", "no")
		d.Events = []event{added{1}}
		return d
	}

	divergences, err := agg.Verify(ctx, "a")
	if err != nil {
		t.Fatalf("Verify returned an error instead of a divergence: %v", err)
	}
	if len(divergences) != 1 || divergences[0].Aspect != ledger.Emitted {
		t.Fatalf("Verify reported %v; a stored refusal that today's code refuses with events is an %s divergence",
			divergences, ledger.Emitted)
	}
}
