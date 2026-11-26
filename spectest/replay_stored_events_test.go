package spectest_test

// The ledger stores a decision's events before it folds them, so the history a
// stream replays is the events Decide produced. An Evolve that writes through
// a reference an event carries must not rewrite that history, and D5 must
// judge the replay of the stored events rather than of the values the live fold
// left behind.

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

type replayStoredMsg struct{ N int }

// replayStoredEvent carries a pointer, so a struct copy handed to Evolve shares
// the value in the decision's event slice: a write through it rewrites the
// event Decide returned.
type replayStoredEvent struct{ N *int }

// replayStoredEvolve rewrites the event it is given before reading it. The
// write is visible in the live fold, but the event the ledger stored holds the
// value Decide produced.
func replayStoredEvolve(total int, e replayStoredEvent) int {
	*e.N *= 2
	return total + *e.N
}

func replayStoredMachine() spectest.Machine[int, replayStoredMsg, int, replayStoredEvent, struct{}] {
	return spectest.Machine[int, replayStoredMsg, int, replayStoredEvent, struct{}]{
		Decider: cairn.Decider[int, replayStoredMsg, int, replayStoredEvent, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(total int, m replayStoredMsg) cairn.Decision[int, replayStoredEvent, struct{}] {
				n := m.N
				return cairn.Accept[int, replayStoredEvent, struct{}](total+m.N, []replayStoredEvent{{N: &n}})
			},
			Evolve: replayStoredEvolve,
		},
		Messages: func(t *rapid.T, _ int) replayStoredMsg {
			return replayStoredMsg{N: rapid.IntRange(1, 10).Draw(t, "n")}
		},
	}
}

// A stream whose stored events replay to the state the run reached must pass
// D5, even though Evolve writes into the event value it was handed.
func TestTheStoredEventsOfAMutatingEvolveReplayToTheRun(t *testing.T) {
	if msg, failed, err := spectest.Fails(t, replayStoredMachine().Check); err != nil || failed {
		t.Fatalf("the ledger replays this stream to the state the run reached, so the replay rule must not fail it: err=%v %s", err, msg)
	}
}

type replayCodecEvent struct {
	N    int
	Text *string
}

func replayCodecMachine() spectest.Machine[int, int, int, replayCodecEvent, struct{}] {
	events := codec.New[replayCodecEvent]()
	codec.Register[replayCodecEvent](events, "replay_codec_event", 1)
	return spectest.Machine[int, int, int, replayCodecEvent, struct{}]{
		Decider: cairn.Decider[int, int, int, replayCodecEvent, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(total int, m int) cairn.Decision[int, replayCodecEvent, struct{}] {
				text := "stored"
				return cairn.Accept[int, replayCodecEvent, struct{}](total+m, []replayCodecEvent{{N: m, Text: &text}})
			},
			Evolve: func(total int, e replayCodecEvent) int {
				// Write a string the codec cannot keep into the live event; the
				// event the ledger stores still holds "stored".
				*e.Text = "bad\xff"
				return total + e.N
			},
		},
		Messages: func(t *rapid.T, _ int) int { return rapid.IntRange(1, 10).Draw(t, "n") },
		Codecs:   &codec.Set[int, int, replayCodecEvent, struct{}]{Events: events},
	}
}

// D6 judges the event the ledger stores, not the value the live fold left
// behind: the stream's history holds the string Decide produced.
func TestTheCodecRoundTripSeesTheStoredEventNotTheFoldedOne(t *testing.T) {
	if msg, failed, err := spectest.Fails(t, replayCodecMachine().Check); err != nil || failed {
		t.Fatalf("the ledger stores this event as Decide produced it, so %s must not fail it: err=%v %s",
			spectest.Encodable.ID, err, msg)
	}
}

// D5 still catches a replay that genuinely ends elsewhere. Here Evolve depends
// on a call count, so folding the stored events in the replay reaches a
// different state from the live run, whatever the event values are.
func TestAReplayThatEndsElsewhereStillFailsWhenAnEventIsRewritten(t *testing.T) {
	m := replayStoredMachine()
	calls := 0
	m.Decider.Evolve = func(total int, e replayStoredEvent) int {
		calls++
		*e.N++
		return total + *e.N + calls
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Replayable.ID+" violated") {
		t.Fatalf("a replay that ends somewhere else must fail %s, got err=%v failed=%v:\n%s",
			spectest.Replayable.ID, err, failed, msg)
	}
}
