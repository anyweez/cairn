package spectest

import (
	"encoding"
	"encoding/json"
	"reflect"
	"unsafe"
)

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
// shallow copy of its struct, as a Go assignment would carry it, unless the
// struct's own JSON encoding writes it. encoding/json ignores an unexported
// field, so a type without its own MarshalJSON or MarshalText cannot store one;
// a type with its own encoding can, and the ledger then hands each Decide call
// an independent value. The copy follows it there.
func clone[T any](v T) T {
	c := cloneValue(reflect.ValueOf(v), map[visit]reflect.Value{})
	if !c.IsValid() {
		return v
	}
	return c.Interface().(T)
}

var (
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
)

// encodesItself reports whether t decides its own JSON bytes. Such a type may
// write a field with an unexported name, which encoding/json itself never does.
func encodesItself(t reflect.Type) bool {
	if t.Implements(jsonMarshaler) || t.Implements(textMarshaler) {
		return true
	}
	// json uses a pointer-receiver marshaller when the value is addressable, and
	// a message is often reached through a pointer.
	p := reflect.PointerTo(t)
	return p.Implements(jsonMarshaler) || p.Implements(textMarshaler)
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
		encoded := encodesItself(v.Type())
		for i := range v.NumField() {
			field := out.Field(i)
			src := v.Field(i)
			if !field.CanSet() {
				// The whole value was assigned first, so a field encoding/json
				// does not write stays shared, as a Go assignment leaves it.
				if !encoded {
					continue
				}
				// An unexported name is not settable through reflect, but out is
				// addressable: address the field it already holds and set that.
				field = reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
				src = field
			}
			field.Set(cloneValue(src, seen))
		}
		return out
	}
	// The remaining kinds are values json stores by copying them, or that
	// cannot be copied at all (a func's closure, a channel), which a message
	// should not reach through.
	return v
}
