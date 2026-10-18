package spectest

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
)

// Two distinct map keys whose pointees are equal render alike ("&7"), and the
// map's entries are sorted by that rendering, so the association between a key
// and its value is lost. Swapping the two values is a change to the state's
// content and the fingerprint must show it.
func TestFingerprintSeesAPointerKeySwap(t *testing.T) {
	k1, k2 := 7, 7
	p1, p2 := &k1, &k2
	m := map[*int]string{p1: "a", p2: "b"}
	before := fingerprint(m)
	m[p1], m[p2] = m[p2], m[p1]
	if after := fingerprint(m); after == before {
		t.Fatalf("swapping the values of two keys that render alike left the fingerprint unchanged: %s", before)
	}
}

// The fingerprint's blindness above is a hole in D4: a machine whose Evolve
// swaps the values of a pointer-keyed map it shares with the state it was given
// mutates its input, and Isolated must catch it. The same key pointers are
// used on every Initial() call, so Replayable has nothing to disagree about and
// D4 is the only rule that can fire.
func TestIsolatedCatchesAPointerKeySwap(t *testing.T) {
	k1, k2 := 7, 7
	p1, p2 := &k1, &k2
	m := Machine[map[*int]string, int, int, bool, struct{}]{
		Decider: cairn.Decider[map[*int]string, int, int, bool, struct{}]{
			Initial: func() map[*int]string {
				return map[*int]string{p1: "a", p2: "b"}
			},
			Decide: func(s map[*int]string, msg int) cairn.Decision[int, bool, struct{}] {
				return cairn.Accept[int, bool, struct{}](0, []bool{true})
			},
			Evolve: func(s map[*int]string, ev bool) map[*int]string {
				s[p1], s[p2] = s[p2], s[p1]
				return s
			},
		},
		Messages: func(t *rapid.T, _ map[*int]string) int { return rapid.IntRange(1, 2).Draw(t, "msg") },
	}
	msg, failed := Fails(t, m.Check)
	if !failed || !strings.Contains(msg, Isolated.ID+" violated") {
		t.Fatalf("Evolve mutating a shared pointer-keyed map must be caught by %s, got failed=%v: %s",
			Isolated.ID, failed, msg)
	}
}
