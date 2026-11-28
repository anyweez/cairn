package ledger_test

import (
	"errors"
	"strings"
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

	record := func(t *testing.T, agg counterAgg, msgs ...add) {
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
		setup  func(*testing.T, counterAgg)
		mutate func(counterAgg)
	}{
		{
			name: "an accepted message is now refused", aspect: ledger.Refused, seq: 1,
			setup:  func(t *testing.T, agg counterAgg) { record(t, agg, add{3, 100}) },
			mutate: always(cairn.Refuse[int, event, effect]("over_cap", "no")),
		},
		{
			name: "a refused message is now accepted", aspect: ledger.Refused, seq: 1,
			setup:  func(t *testing.T, agg counterAgg) { record(t, agg, add{9, 4}) },
			mutate: always(cairn.Accept[int, event, effect](1, []event{added{1}}, announce{1})),
		},
		{
			name: "both refuse with different codes", aspect: ledger.Refused, seq: 1,
			setup:  func(t *testing.T, agg counterAgg) { record(t, agg, add{9, 4}) },
			mutate: always(cairn.Refuse[int, event, effect]("other", "different")),
		},
		{
			name: "the result differs", aspect: ledger.Returned, seq: 1,
			setup:  func(t *testing.T, agg counterAgg) { record(t, agg, add{3, 100}) },
			mutate: always(cairn.Accept[int, event, effect](4, []event{added{3}}, announce{3})),
		},
		{
			name: "the events differ", aspect: ledger.Emitted, seq: 1,
			setup:  func(t *testing.T, agg counterAgg) { record(t, agg, add{3, 100}) },
			mutate: always(cairn.Accept[int, event, effect](3, []event{added{99}}, announce{3})),
		},
		{
			name: "the effects differ", aspect: ledger.Effected, seq: 1,
			setup:  func(t *testing.T, agg counterAgg) { record(t, agg, add{3, 100}) },
			mutate: always(cairn.Accept[int, event, effect](3, []event{added{3}}, announce{99})),
		},
		{
			// The stored refusal is inert, so the same code with an effect is
			// still a divergence rather than a consequence of the verdict.
			name: "a same-code refusal gains effects", aspect: ledger.Effected, seq: 1,
			setup: func(t *testing.T, agg counterAgg) { record(t, agg, add{9, 4}) },
			mutate: func(agg counterAgg) {
				agg.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
					d := cairn.Refuse[int, event, effect]("over_cap", "no")
					d.Effects = []effect{announce{1}}
					return d
				}
			},
		},
		{
			name: "the message no longer decodes", aspect: ledger.Undecodable, seq: 1,
			setup: func(t *testing.T, agg counterAgg) { record(t, agg, add{3, 100}) },
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
			setup: func(t *testing.T, agg counterAgg) { record(t, agg, add{3, 100}, add{4, 100}) },
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
			tc.setup(t, agg)
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

// Verify replays history written by earlier code, so a Decider meeting a
// message or event shape it no longer handles is the case the audit most has
// to report. A panic there is a finding about one entry: Verify must not let
// it escape, must say which entry it happened at, and must keep the
// divergences it already found. The write path recovers the same panic in
// record, so ending the caller on one here would be the audit doing worse than
// the writer.
func TestVerifyReportsADeciderPanicAtTheEntry(t *testing.T) {
	t.Parallel()

	// The first entry is re-decided with a changed result, so every case that
	// reaches it has a divergence in hand before the panic.
	diverging := func(s counter, m msg) cairn.Decision[int, event, effect] {
		d := decide(s, m)
		if s.Total == 0 {
			d.Result = 424242
		}
		return d
	}

	cases := []struct {
		name  string
		found int    // divergences the panic must leave in place
		want  string // what the error must name
		setup func(t *testing.T, agg counterAgg)
	}{
		{
			name: "Initial", want: "counter/a",
			setup: func(t *testing.T, agg counterAgg) {
				agg.Decider.Initial = func() counter { panic("today's code has no initial state") }
			},
		},
		{
			name: "Decide", found: 1, want: "counter/a#2",
			setup: func(t *testing.T, agg counterAgg) {
				agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
					if s.Total != 0 {
						panic("today's decider cannot decide a message it used to")
					}
					return diverging(s, m)
				}
			},
		},
		{
			name: "Fold", found: 1, want: "counter/a#2",
			setup: func(t *testing.T, agg counterAgg) {
				agg.Decider.Decide = diverging
				agg.Decider.Evolve = func(s counter, e event) counter {
					if s.Total != 0 {
						panic("today's fold cannot fold an event it used to")
					}
					return counterDecider.Evolve(s, e)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			agg := newCounter(t)
			for i := 0; i < 2; i++ {
				if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
					t.Fatal(err)
				}
			}
			tc.setup(t, agg)

			var (
				divergences []ledger.Divergence
				err         error
			)
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("Verify let a panic in %s escape: %v", tc.name, p)
					}
				}()
				divergences, err = agg.Verify(ctx, "a")
			}()

			if len(divergences) != tc.found {
				t.Fatalf("%s panic changed what the audit found: got %v, want %d divergence(s)", tc.name, divergences, tc.found)
			}
			if tc.found == 1 && divergences[0].Seq != 1 {
				t.Fatalf("%s panic lost the divergence found at a#1: got %v", tc.name, divergences)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s panic reported err=%v, want one naming %s", tc.name, err, tc.want)
			}
		})
	}
}

// VerifyAll keeps the divergences from earlier streams when a later stream's
// audit panics, the same way it already keeps them when Verify returns an
// error for one.
func TestVerifyAllKeepsEarlierStreamsWhenALaterAuditPanics(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	// Streams are audited in id order, so a's entry is re-decided first.
	for _, id := range []string{"a", "b"} {
		if _, err := agg.Handle(ctx, id, add{1, 10}); err != nil {
			t.Fatal(err)
		}
	}
	decided := 0
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		decided++
		if decided == 2 {
			panic("today's decider cannot decide a message it used to")
		}
		d := decide(s, m)
		d.Result = 424242
		return d
	}

	divergences, err := agg.VerifyAll(ctx)
	if err == nil || !strings.Contains(err.Error(), "counter/b#1") {
		t.Fatalf("VerifyAll reported err=%v, want one naming counter/b#1", err)
	}
	if len(divergences) != 1 || divergences[0].Stream.ID != "a" || divergences[0].Aspect != ledger.Returned {
		t.Fatalf("VerifyAll lost stream a's divergence when stream b's audit panicked: got %v", divergences)
	}
}
