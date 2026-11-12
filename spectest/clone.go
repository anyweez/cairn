package spectest

import "reflect"

// clone returns a deep copy of v: every pointer, interface, map, slice and
// array it reaches is copied, so a write through one of them changes the copy
// only.
//
// The ledger encodes the caller's message and gives Decide the value decoded
// back from that envelope, so every Decide call starts from the message as it
// was sent (ledger/message_as_sent_test.go is the contract). D1 compares two
// calls, so each must get its own such copy: handed one live value, a Decide
// that normalises a map, slice or pointer field in place would change the input
// its second call receives, and be reported as nondeterministic.
//
// A value reached through an unexported struct field is carried over by the
// shallow copy of its struct, as a Go assignment would carry it. The codec that
// stores a message does not write an unexported field, and a non-zero one fails
// D6, so there is no stored message for it to be copied faithfully to.
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
		// The whole value first, so a field this cannot set — one reached
		// through an unexported name — is still carried over as a Go
		// assignment carries it.
		out.Set(v)
		for i := range v.NumField() {
			if !out.Field(i).CanSet() {
				continue
			}
			out.Field(i).Set(cloneValue(v.Field(i), seen))
		}
		return out
	}
	// The remaining kinds are values json stores by copying them, or that
	// cannot be copied at all (a func's closure, a channel), which a message
	// should not reach through.
	return v
}
