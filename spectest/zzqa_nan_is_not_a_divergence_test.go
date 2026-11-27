package spectest_test

// A state or decision can carry a float64 NaN that the same computation
// produced twice — a ratio of two zeroes is the ordinary way one reaches it.
// The checker compares two such values with Go's equality, where NaN is not
// equal to itself, so it reports a divergence between two values that are the
// same bits and prints the same text. D5 and D1 are meant to catch a replay or
// a decision that actually differs, not a value that cannot equal itself.

import (
	"math"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

// zzqaRate is a state whose fold can reach a NaN: a rate is a ratio of two
// deltas, and a step where both are zero is 0/0.
type zzqaRate struct{ Rate float64 }

type zzqaStep struct{ Zero bool }

func zzqaRateMachine() spectest.Machine[zzqaRate, zzqaStep, int, bool, struct{}] {
	return spectest.Machine[zzqaRate, zzqaStep, int, bool, struct{}]{
		Decider: cairn.Decider[zzqaRate, zzqaStep, int, bool, struct{}]{
			Initial: func() zzqaRate { return zzqaRate{} },
			Decide: func(s zzqaRate, msg zzqaStep) cairn.Decision[int, bool, struct{}] {
				return cairn.Accept[int, bool, struct{}](0, []bool{msg.Zero})
			},
			Evolve: func(s zzqaRate, zero bool) zzqaRate {
				numerator, denominator := 0.0, 1.0
				if zero {
					denominator = 0
				}
				s.Rate = numerator / denominator
				return s
			},
		},
		Messages: func(t *rapid.T, _ zzqaRate) zzqaStep { return zzqaStep{Zero: true} },
	}
}

// Folding the events a run emitted gives the NaN the run itself reached, so D5
// has nothing to report. The checker must not call the two NaNs different.
func TestANaNStateReplaysAsTheStateItReached(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaRateMachine().Check)
	if err != nil {
		t.Fatalf("the check did not finish: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("a state of NaN replays as the same NaN, so D5 must not report a divergence:\n%s", msg)
	}
}

// zzqaAverage is a decision result that is a ratio, the same way the state is.
type zzqaAverage float64

func TestANaNDecisionIsDeterministic(t *testing.T) {
	m := spectest.Machine[zzqaRate, zzqaStep, zzqaAverage, int, struct{}]{
		Decider: cairn.Decider[zzqaRate, zzqaStep, zzqaAverage, int, struct{}]{
			Initial: func() zzqaRate { return zzqaRate{} },
			Decide: func(s zzqaRate, msg zzqaStep) cairn.Decision[zzqaAverage, int, struct{}] {
				zero, zeroAgain := 0.0, 0.0
				return cairn.Accept[zzqaAverage, int, struct{}](zzqaAverage(zero/zeroAgain), nil)
			},
			Evolve: func(s zzqaRate, _ int) zzqaRate { return s },
		},
		Messages: func(t *rapid.T, _ zzqaRate) zzqaStep { return zzqaStep{} },
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil {
		t.Fatalf("the check did not finish: %v\n%s", err, msg)
	}
	if failed {
		t.Fatalf("the same state and message returned the same NaN twice, so D1 must not report them as different:\n%s", msg)
	}
}

// The check's teeth: a fold that really does end somewhere else is still a D5
// violation, even when one of the two states is a NaN. A comparison that
// accepted every NaN pair must not also accept NaN against a number.
func TestARealReplayDivergenceStillFailsD5(t *testing.T) {
	calls := 0
	m := zzqaRateMachine()
	m.Decider.Evolve = func(s zzqaRate, _ bool) zzqaRate {
		calls++
		if calls%2 == 1 {
			s.Rate = math.NaN()
		} else {
			s.Rate = 1
		}
		return s
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Replayable.ID+" violated") {
		t.Fatalf("a replay that reaches a different value must still fail %s, got err=%v failed=%v:\n%s",
			spectest.Replayable.ID, err, failed, msg)
	}
}
