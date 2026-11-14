package spectest

import (
	"encoding/json"
	"testing"
)

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

// encodedHidden keeps its references private and stores them through its own
// JSON encoding, the way a message that treats its fields as private does.
type encodedHidden struct {
	items    map[string]int
	list     []int
	p        *int
	anything any
}

func (m encodedHidden) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Items    map[string]int `json:"items"`
		List     []int          `json:"list"`
		P        *int           `json:"p"`
		Anything any            `json:"anything"`
	}{m.items, m.list, m.p, m.anything})
}

func (m *encodedHidden) UnmarshalJSON(data []byte) error {
	var wire struct {
		Items    map[string]int `json:"items"`
		List     []int          `json:"list"`
		P        *int           `json:"p"`
		Anything any            `json:"anything"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	m.items = wire.Items
	m.list = wire.List
	m.p = wire.P
	m.anything = wire.Anything
	return nil
}

// A field the message's own encoding writes is stored by the ledger, which
// gives each Decide call its own decoded value, so the copy must not share it.
func TestCloneCopiesUnexportedFieldsItsJSONEncodingWrites(t *testing.T) {
	n := 1
	m := encodedHidden{items: map[string]int{"a": 1}, list: []int{1}, p: &n, anything: map[string]int{"b": 1}}
	c := clone(m)

	c.items["a"] = 2
	c.list[0] = 2
	*c.p = 2
	c.anything.(map[string]int)["b"] = 2

	if m.items["a"] != 1 {
		t.Errorf("a write to the copy's unexported map reached the original: %v", m.items)
	}
	if m.list[0] != 1 {
		t.Errorf("a write to the copy's unexported slice element reached the original: %v", m.list)
	}
	if n != 1 {
		t.Errorf("a write through the copy's unexported pointer reached the original: %d", n)
	}
	if m.anything.(map[string]int)["b"] != 1 {
		t.Errorf("a write to the map behind the copy's unexported interface reached the original: %v", m.anything)
	}
}

// A field encoding/json does not write is not stored by the ledger, so the
// value is carried by assignment, as a Go copy of the struct carries it.
func TestCloneLeavesUnexportedFieldsItsJSONEncodingDoesNotWrite(t *testing.T) {
	type msg struct {
		items    map[string]int
		list     []int
		p        *int
		anything any
	}
	n := 1
	m := msg{items: map[string]int{"a": 1}, list: []int{1}, p: &n, anything: map[string]int{"b": 1}}
	c := clone(m)

	c.items["a"] = 2
	c.list[0] = 2
	*c.p = 2
	c.anything.(map[string]int)["b"] = 2

	if m.items["a"] != 2 || m.list[0] != 2 || n != 2 || m.anything.(map[string]int)["b"] != 2 {
		t.Fatalf("clone split a field encoding/json cannot write; the value should stay shared by assignment: %+v", m)
	}
}
