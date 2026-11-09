package spectest_test

// The ledger encodes the caller's message before Decide sees it and gives
// Decide the message decoded back from that envelope, so a decider that
// normalises a field of its message in place is supported: each call gets a
// fresh copy of what the caller sent, and a replay re-decides the same stored
// message (ledger/message_as_sent_test.go is the contract).
//
// spectest judges D1 by calling Decide twice with the same message value. A
// map, slice or pointer field is shared between the two calls, so the second
// call sees whatever the first call wrote. A decider that counts what it has
// seen in its message is then reported as nondeterministic, although every
// call the ledger makes starts from the message the caller sent.

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/spectest"
)

type zzqaTallyMsg struct{ Seen map[string]int }

type zzqaTallyDecision = cairn.Decision[int, int, struct{}]

// zzqaTallyDecide writes into the message it is given, the way a decider that
// normalises a caller's field in place does, and returns the count it wrote.
func zzqaTallyDecide(_ int, m zzqaTallyMsg) zzqaTallyDecision {
	m.Seen["calls"]++
	return cairn.Accept[int, int, struct{}](m.Seen["calls"], []int{1})
}

func zzqaTallyMachine() spectest.Machine[int, zzqaTallyMsg, int, int, struct{}] {
	return spectest.Machine[int, zzqaTallyMsg, int, int, struct{}]{
		Decider: cairn.Decider[int, zzqaTallyMsg, int, int, struct{}]{
			Initial: func() int { return 0 },
			Decide:  zzqaTallyDecide,
			Evolve:  func(n int, ev int) int { return n + ev },
		},
		Messages: func(t *rapid.T, _ int) zzqaTallyMsg {
			return zzqaTallyMsg{Seen: map[string]int{}}
		},
	}
}

// The ledger would give this decider a fresh message on every call, so it is
// deterministic over the input it is actually given. D1 must not fail it.
func TestDecideMayNormaliseTheMessageItIsGiven(t *testing.T) {
	msg, failed, err := spectest.Fails(t, zzqaTallyMachine().Check)
	if err != nil {
		t.Fatalf("the check did not finish cleanly: %v", err)
	}
	if failed {
		t.Fatalf("a decider the ledger supports was failed by the spec check:\n%s", msg)
	}
}

// The other side of the rule: a decider whose answer depends on something
// outside its inputs is still nondeterministic, and D1 must catch it.
func TestDecideStillCannotDependOnSomethingOutsideItsInputs(t *testing.T) {
	m := zzqaTallyMachine()
	calls := 0
	m.Decider.Decide = func(n int, msg zzqaTallyMsg) zzqaTallyDecision {
		calls++
		return cairn.Accept[int, int, struct{}](calls, []int{1})
	}
	msg, failed, err := spectest.Fails(t, m.Check)
	if err != nil || !failed || !strings.Contains(msg, spectest.Deterministic.ID+" violated") {
		t.Fatalf("a decider that depends on a call count must fail %s, got err=%v failed=%v:\n%s",
			spectest.Deterministic.ID, err, failed, msg)
	}
}
