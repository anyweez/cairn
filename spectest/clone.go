package spectest

import (
	"reflect"
	"unsafe"
)

// clone returns a deep copy of v: every pointer, interface, map, slice and
// array it reaches is copied, so a write through one of them changes the copy
// only.
//
// The ledger encodes the caller's message and gives Decide the value decoded
// back from that envelope, so every Decide call starts from the message as it
// was sent (ledger/message_as_sent_test.go is the contract), and the entry that
// is stored holds the message as sent. D1 compares two calls, so each must get
// its own copy of that value; D6 round-trips the value itself. Handed one live
// message, a Decide that normalises a field in place would change the input its
// second call receives, and rewrite the message D6 compares against what the
// codec kept: two false verdicts about a decider the ledger supports.
//
// The same isolation is what a rule's check, a refinement model and the
// Messages generator get: the run hands each callback a clone of the state,
// message or decision it owns, so a check that sorts a slice it was given
// cannot change what later messages are decided from or what D5 replays.
//
// A field with an unexported name is copied like any other, though
// encoding/json neither writes it nor reads it back. The checker holds the
// message as sent, and a field the codec drops is a loss D6 reports; sharing it
// would let a Decide call's write to its copy erase the evidence. A type whose
// own JSON code does reach such a field — one with MarshalJSON, MarshalText or
// UnmarshalJSON — is the same case, not a separate one.
func clone[T any](v T) T {
	c := cloneValue(reflect.ValueOf(v), map[visit]reflect.Value{})
	if !c.IsValid() {
		return v
	}
	return c.Interface().(T)
}

// cloneValue copies one value, keeping the shape of its object graph: a map,
// slice or pointer reached twice is copied once, and a cycle comes back as a
// cycle. seen maps each original object to its copy.
func cloneValue(v reflect.Value, seen map[visit]reflect.Value) reflect.Value {
	if !v.IsValid() {
		return v
	}
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneValue(v.Elem(), seen))
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		k := visitKey(v)
		if out, ok := seen[k]; ok {
			return out
		}
		out := reflect.New(v.Type().Elem())
		seen[k] = out
		out.Elem().Set(cloneValue(v.Elem(), seen))
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		k := visitKey(v)
		if out, ok := seen[k]; ok {
			return out
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		seen[k] = out
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(cloneValue(iter.Key(), seen), cloneValue(iter.Value(), seen))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		k := visitKey(v)
		if out, ok := seen[k]; ok {
			return out
		}
		// Capacity is not copied: a write into the original's spare capacity
		// cannot be reached from the copy, which is the isolation D1 needs.
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		seen[k] = out
		for i := range v.Len() {
			out.Index(i).Set(cloneValue(v.Index(i), seen))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			out.Index(i).Set(cloneValue(v.Index(i), seen))
		}
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			field := out.Field(i)
			if !field.CanSet() {
				// An unexported name is not settable through reflect, but out is
				// addressable: address the field it already holds, which the
				// assignment above gave the original's reference, and set that.
				field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
			}
			field.Set(cloneValue(field, seen))
		}
		return out
	}
	// The remaining kinds are values json stores by copying them, or that
	// cannot be copied at all (a func's closure, a channel), which a message
	// should not reach through.
	return v
}
