package ledger_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
)

type counterAgg = *ledger.Aggregate[counter, msg, int, event, effect]

// Verify reports each kind of disagreement with the recorded history as its
// own Aspect: a changed verdict, a changed result, changed events, changed
// effects, or a message that no longer decodes. Once the verdict differs the
// rest follow from it, so a re-decided message reports exactly one aspect, and
// the state used for the next message comes from the stored events rather than
// the re-decided ones. This walks each aspect and checks both.
func TestVerifyReportsOneDivergencePerAspect(t *testing.T) {
	t.Parallel()

	record := func(agg counterAgg, msgs ...add) {
		t.Helper()
		for _, m := range msgs {
			// A refusal is recorded like any other decision, so it is expected
			// here; anything else is not.
			if _, err := agg.Handle(ctx, "a", m); err != nil {
				if refusal := new(*cairn.Refusal); !errors.As(err, refusal) {
					t.Fatal(err)
				}
			}
		}
	}
	// always makes every message decide the same thing, so the disagreement is
	// the one under test rather than a consequence of the state.
	always := func(d cairn.Decision[int, event, effect]) func(counterAgg) {
		return func(agg counterAgg) {
			agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] { return d }
		}
	}

	cases := []struct {
		name   string
		aspect ledger.Aspect
		seq    int64
		setup  func(counterAgg)
		mutate func(counterAgg)
	}{
		{
			name: "an accepted message is now refused", aspect: ledger.Refused, seq: 1,
			setup:  func(agg counterAgg) { record(agg, add{3, 100}) },
			mutate: always(cairn.Refuse[int, event, effect]("over_cap", "no")),
		},
		{
			name: "a refused message is now accepted", aspect: ledger.Refused, seq: 1,
			setup:  func(agg counterAgg) { record(agg, add{9, 4}) },
			mutate: always(cairn.Accept[int, event, effect](1, []event{added{1}}, announce{1})),
		},
		{
			name: "both refuse with different codes", aspect: ledger.Refused, seq: 1,
			setup:  func(agg counterAgg) { record(agg, add{9, 4}) },
			mutate: always(cairn.Refuse[int, event, effect]("other", "different")),
		},
		{
			name: "the result differs", aspect: ledger.Returned, seq: 1,
			setup:  func(agg counterAgg) { record(agg, add{3, 100}) },
			mutate: always(cairn.Accept[int, event, effect](4, []event{added{3}}, announce{3})),
		},
		{
			name: "the events differ", aspect: ledger.Emitted, seq: 1,
			setup:  func(agg counterAgg) { record(agg, add{3, 100}) },
			mutate: always(cairn.Accept[int, event, effect](3, []event{added{99}}, announce{3})),
		},
		{
			name: "the effects differ", aspect: ledger.Effected, seq: 1,
			setup:  func(agg counterAgg) { record(agg, add{3, 100}) },
			mutate: always(cairn.Accept[int, event, effect](3, []event{added{3}}, announce{99})),
		},
		{
			name: "the message no longer decodes", aspect: ledger.Undecodable, seq: 1,
			setup: func(agg counterAgg) { record(agg, add{3, 100}) },
			mutate: func(agg counterAgg) {
				// "add" is at version 2 today with no upcaster from version 1,
				// so the stored envelope cannot reach the current shape.
				broken := codec.New[msg]()
				codec.Register[add](broken, "add", 2)
				agg.Codecs.Messages = broken
			},
		},
		{
			// A divergent first message must not change the state the second is
			// decided against: entry 1's events disagree, entry 2's do not.
			name: "a divergence does not cascade", aspect: ledger.Emitted, seq: 1,
			setup: func(agg counterAgg) { record(agg, add{3, 100}, add{4, 100}) },
			mutate: func(agg counterAgg) {
				agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
					d := decide(s, m)
					if s.Total == 0 {
						d.Events = []event{added{99}}
					}
					return d
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			agg := newCounter(t)
			tc.setup(agg)
			tc.mutate(agg)
			divergences, err := agg.Verify(ctx, "a")
			if err != nil {
				t.Fatal(err)
			}
			if len(divergences) != 1 || divergences[0].Aspect != tc.aspect || divergences[0].Seq != tc.seq {
				t.Fatalf("got %v, want one %s divergence at seq %d", divergences, tc.aspect, tc.seq)
			}
		})
	}
}
