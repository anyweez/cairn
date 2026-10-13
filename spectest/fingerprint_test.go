package spectest

import (
	"strings"
	"testing"
)

// Isolated relies on fingerprint changing whenever a write lands anywhere the
// state reaches, however it is reached.
func TestFingerprintSeesWritesThroughSharedReferences(t *testing.T) {
	type state struct {
		Map   map[string]int
		Slice []int
		Ptr   *int
		Any   any
		inner []string // unexported
	}
	n := 1
	cases := []struct {
		name  string
		write func(s *state)
	}{
		{"map", func(s *state) { s.Map["a"]++ }},
		{"slice element", func(s *state) { s.Slice[0]++ }},
		{"pointer", func(s *state) { *s.Ptr++ }},
		{"map behind an interface", func(s *state) { s.Any.(map[string]int)["b"]++ }},
		{"unexported field", func(s *state) { s.inner[0] = "changed" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := state{
				Map: map[string]int{"a": 1}, Slice: []int{1}, Ptr: &n,
				Any: map[string]int{"b": 1}, inner: []string{"x"},
			}
			before := fingerprint(s)
			// A copy shares every reference with s, as a Decide given s does.
			alias := s
			c.write(&alias)
			if fingerprint(s) == before {
				t.Fatalf("a write through the copy's %s left the fingerprint unchanged", c.name)
			}
		})
	}
}

func TestFingerprintOrdersMaps(t *testing.T) {
	a := map[string]int{}
	b := map[string]int{}
	for i, k := range []string{"x", "y", "z", "w"} {
		a[k] = i
	}
	for i, k := range []string{"w", "z", "y", "x"} {
		b[k] = 3 - i
	}
	if fingerprint(a) != fingerprint(b) {
		t.Fatalf("equal maps fingerprint differently: %s vs %s", fingerprint(a), fingerprint(b))
	}
}

// Keys that render alike (pointers to equal values) must not let map
// iteration order leak into the fingerprint, or an untouched state would
// fingerprint differently twice and fail Isolated.
func TestFingerprintIsStableWhenKeysRenderAlike(t *testing.T) {
	m := map[*int]string{}
	for i := range 8 {
		k := 7
		m[&k] = string(rune('a' + i))
	}
	first := fingerprint(m)
	for range 50 {
		if got := fingerprint(m); got != first {
			t.Fatalf("one map fingerprinted two ways:\n%s\n%s", first, got)
		}
	}
}

// Appending to the given state's slice writes into its spare capacity
// without changing its length; that write must still change its fingerprint.
func TestFingerprintSeesAWriteIntoSpareCapacity(t *testing.T) {
	s := make([]int, 2, 4)
	before := fingerprint(s)
	_ = append(s, 9)
	if fingerprint(s) == before {
		t.Fatal("an append into spare capacity left the fingerprint unchanged")
	}
}

// A value that contains itself renders as a cycle instead of recursing until
// the stack overflows.
func TestFingerprintTerminatesOnCycles(t *testing.T) {
	type node struct{ Next *node }
	p := &node{}
	p.Next = p

	m := map[string]any{}
	m["self"] = m

	s := make([]any, 1)
	s[0] = s

	for name, v := range map[string]any{"pointer": p, "map": m, "slice": s} {
		if got := fingerprint(v); !strings.Contains(got, "<cycle>") {
			t.Errorf("%s cycle rendered as %s", name, got)
		}
	}
}

// The same reference reached twice side by side is not a cycle.
func TestFingerprintSharedButAcyclic(t *testing.T) {
	shared := map[string]int{"a": 1}
	v := []map[string]int{shared, shared}
	if got := fingerprint(v); strings.Contains(got, "<cycle>") {
		t.Fatalf("a map reached twice was taken for a cycle: %s", got)
	}
}
