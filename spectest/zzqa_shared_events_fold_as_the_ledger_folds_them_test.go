package spectest_test

// The ledger does not fold the events Decide returned: it encodes each one and
// folds the values it decodes back (encodeReadable, then Fold(at.State,
// storedEvents), ledger/aggregate.go). JSON has no way to say that two events
// name one object, so a decision whose two events share a mutable value reaches
// Evolve as two events holding two values — and the same is true of any later
// Load, which folds DecodeAll of the stored envelopes.
//
// spectest folds clone(decision.Events): one clone of the whole slice, which
// keeps the shape of the decision's object graph, so the two events still share
// their value. When Evolve writes through the value it is given — a write the
// ledger allows, because the event it folds is not the state it was given — the
// checker's second fold sees a value the first fold rewrote, and the state the
// check validates is not the state the stream reaches.

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// qaSharedEvent carries the amount it applied, so a decision can say the same
// value twice. Its field is a pointer, which is what lets two events of one
// decision name one value.
type qaSharedEvent struct{ N *int }

// qaSharedRule forbids the state the checker's fold reaches and the ledger's
// does not: an applied amount of 7 is what a second event sees when the first
// event's write reached it.
func qaSharedRule() spec.Rule {
	return spec.Rule{ID: "Q1", Kind: spec.Never, Status: spec.Established,
		Statement: "A message reports having applied 7."}
}

// qaSharedMachine decides two events that share the value 5, and folds each by
// bumping it. In the ledger both events are decoded separately, so each fold
// bumps its own 5 to 6 and the state settles at 6 on every message. In the
// checker they share one value, so the first fold bumps it to 6 and the second
// to 7, and the invariant fires.
func qaSharedMachine() spectest.Machine[int, int, int, qaSharedEvent, struct{}] {
	events := codec.New[qaSharedEvent]()
	codec.Register[qaSharedEvent](events, "qa.shared_event", 1)
	results := codec.New[int]()
	codec.Register[int](results, "qa.shared_result", 1)
	return spectest.Machine[int, int, int, qaSharedEvent, struct{}]{
		Decider: cairn.Decider[int, int, int, qaSharedEvent, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(_ int, _ int) cairn.Decision[int, qaSharedEvent, struct{}] {
				n := 5
				return cairn.Accept[int, qaSharedEvent, struct{}](0, []qaSharedEvent{{N: &n}, {N: &n}})
			},
			Evolve: func(_ int, e qaSharedEvent) int {
				*e.N++
				return *e.N
			},
		},
		Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(1, 1).Draw(t, "msg") },
		Invariants: []spec.Invariant[int]{{Rule: qaSharedRule(), Check: func(s int) spec.Verdict {
			return spec.Check(s != 7, "the message applied %d", s)
		}}},
		Codecs: &codec.Set[int, int, qaSharedEvent, struct{}]{Results: results, Events: events},
	}
}

func TestASharedEventGraphFoldsAsTheLedgerFoldsIt(t *testing.T) {
	m := qaSharedMachine()
	dec := m.Decider.Decide(0, 0)
	if len(dec.Events) != 2 || dec.Events[0].N != dec.Events[1].N {
		t.Fatalf("the reproduction needs one decision whose two events name one value, got %+v", dec.Events)
	}

	// What the ledger folds: each event encoded on its own and decoded back, as
	// encodeReadable does before Fold(at.State, storedEvents).
	envs, err := m.Codecs.Events.EncodeAll(dec.Events)
	if err != nil {
		t.Fatalf("encoding the decision's events: %v", err)
	}
	stored, err := m.Codecs.Events.DecodeAll(envs)
	if err != nil {
		t.Fatalf("decoding the decision's events: %v", err)
	}
	if stored[0].N == stored[1].N {
		t.Fatalf("the codec kept the decision's sharing, so the ledger folds the same graph the checker does")
	}
	ledgerState := m.Decider.Fold(0, stored)
	if _, failed := m.Invariants[0].Check(ledgerState).Failed(); failed {
		t.Fatalf("the rule does not hold on the state the ledger reaches (%d), so the machine is not the one this test is about", ledgerState)
	}

	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed {
		t.Fatalf("the check failed a machine whose stream settles at %d on every message, because it folded a shared value the codec splits:\n%s",
			ledgerState, msg)
	}
}
