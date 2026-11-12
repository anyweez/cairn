package spectest

import "testing"

// D1 hands each Decide call its own copy of the message as sent, so a write
// through any shared reference reaches the copy only.
func TestCloneCopiesEverySharedReference(t *testing.T) {
	type msg struct {
		Map   map[string]int
		Slice []int
		Ptr   *int
		Any   any
	}
	n := 1
	m := msg{Map: map[string]int{"a": 1}, Slice: []int{1}, Ptr: &n, Any: map[string]int{"b": 1}}
	c := clone(m)

	c.Map["a"] = 2
	c.Slice[0] = 2
	*c.Ptr = 2
	c.Any.(map[string]int)["b"] = 2

	if m.Map["a"] != 1 {
		t.Errorf("a write to the copy's map reached the original: %v", m.Map)
	}
	if m.Slice[0] != 1 {
		t.Errorf("a write to the copy's slice element reached the original: %v", m.Slice)
	}
	if n != 1 {
		t.Errorf("a write through the copy's pointer reached the original: %d", n)
	}
	if m.Any.(map[string]int)["b"] != 1 {
		t.Errorf("a write to the map behind the copy's interface reached the original: %v", m.Any)
	}
}

// A map, slice or pointer reached twice is copied once, so the copy keeps the
// object graph's shape rather than duplicating a shared value.
func TestCloneKeepsSharing(t *testing.T) {
	type msg struct {
		A, B map[string]int
	}
	shared := map[string]int{"x": 1}
	c := clone(msg{A: shared, B: shared})
	c.A["x"] = 2
	if c.B["x"] != 2 {
		t.Error("the copy split a map the original reached twice")
	}
}

// A value that contains itself must terminate, and the copy must keep the
// cycle rather than unfolding it.
func TestCloneTerminatesOnCycles(t *testing.T) {
	type node struct {
		Next *node
		Any  any
	}
	p := &node{}
	p.Next = p
	p.Any = p

	c := clone(p)
	if c == p {
		t.Fatal("clone returned the original pointer")
	}
	if c.Next != c || c.Any.(*node) != c {
		t.Fatal("the copy did not keep the original's cycle")
	}
}
