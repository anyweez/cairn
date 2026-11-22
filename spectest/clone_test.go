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

// Different objects can share an address: every empty slice points at the
// runtime's zerobase, a struct and its first field share one, and two named map
// types converted from one another share one backing map. The copy's identity
// key must include the type, or it hands a copy of one to the other's field and
// reflect.Set panics.
func TestCloneDistinguishesObjectsThatShareAnAddress(t *testing.T) {
	t.Run("empty slices", func(t *testing.T) {
		type msg struct {
			Names  []string
			Counts []int
		}
		c := clone(msg{Names: []string{}, Counts: []int{}})
		if c.Names == nil || c.Counts == nil {
			t.Fatalf("an empty slice was copied as nil: %+v", c)
		}
	})

	t.Run("pointer and first field", func(t *testing.T) {
		type head struct{ N int }
		type body struct {
			head
			M int
		}
		type msg struct {
			Whole *body
			Head  *head
		}
		b := &body{}
		c := clone(msg{Whole: b, Head: &b.head})
		if c.Whole == nil || c.Head == nil || c.Whole == b || c.Head == &b.head {
			t.Fatalf("the copy did not keep two pointers apart: %+v", c)
		}
	})

	t.Run("named map types", func(t *testing.T) {
		type m1 map[string]int
		type m2 map[string]int
		a := m1{"x": 1}
		c := clone(struct {
			A m1
			B m2
		}{A: a, B: m2(a)})
		if c.A["x"] != 1 || c.B["x"] != 1 {
			t.Fatalf("a converted map was copied wrong: %+v", c)
		}
	})
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

// A field encoding/json does not write is dropped by the codec, which D6
// reports. The copy must still not share it: a write through it would reach the
// message D6 round-trips, and a decider that zeroed it could make the loss look
// like a value the codec kept, or make Decide's second call see the first one's
// write.
func TestCloneCopiesUnexportedFieldsItsJSONEncodingDoesNotWrite(t *testing.T) {
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

// An embedded unexported struct is part of the message as sent: encoding/json
// promotes its exported fields, so the codec stores them and the ledger gives
// each Decide call an independently decoded map. The copy must reach it too,
// though the field itself has an unexported name.
func TestCloneCopiesAnEmbeddedUnexportedStruct(t *testing.T) {
	type seen struct {
		Seen map[string]int `json:"seen"`
	}
	type msg struct {
		seen
		Note string `json:"note"`
	}
	m := msg{seen: seen{Seen: map[string]int{"calls": 1}}, Note: "rent"}
	c := clone(m)

	c.Seen["calls"] = 2
	if m.Seen["calls"] != 1 {
		t.Fatalf("a write to the copy's embedded unexported map reached the original: %v", m.Seen)
	}
}
