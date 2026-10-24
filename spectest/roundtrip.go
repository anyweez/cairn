package spectest

import (
	"reflect"
	"time"
)

var timeType = reflect.TypeFor[time.Time]()

// equalRoundTripped reports whether a value and what its codec or snapshot
// round trip gave back are the same value.
//
// It is reflect.DeepEqual except for a time.Time, which is compared by its
// instant, because that is the notion of "read back unchanged" the codec and
// the ledger hold themselves to. json.Marshal writes an instant and a zone
// offset but neither the monotonic clock reading nor the *Location it came
// from, so a time the ledger records, snapshots and replays exactly fails
// DeepEqual. codec.CheckStorable accepts exactly the times whose instant
// survives, and queueSnapshot runs it before writing a snapshot; this is that
// rule, applied to the pair rather than to one value.
//
// Everything else is DeepEqual's answer, so a field a codec or a snapshot
// drops — a `json:"-"` tag, an unexported field — is still reported, and so is
// a time whose instant moves.
func equalRoundTripped(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	return equalRoundTripValue(reflect.ValueOf(a), reflect.ValueOf(b), map[visit]bool{})
}

// equalRoundTripValue walks a pair of the same type, handing each kind to its
// own comparison. seen holds the pointers, maps and slices on the path, so a
// value that contains itself terminates the way DeepEqual's walk does.
func equalRoundTripValue(a, b reflect.Value, seen map[visit]bool) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	if a.Type() == timeType {
		return sameInstant(a, b)
	}
	switch a.Kind() {
	case reflect.Interface, reflect.Pointer:
		return equalThrough(a, b, seen)
	case reflect.Struct:
		return equalFields(a, b, seen)
	case reflect.Array:
		return equalElements(a, b, seen)
	case reflect.Slice:
		return equalSlice(a, b, seen)
	case reflect.Map:
		return equalMap(a, b, seen)
	case reflect.Func:
		// DeepEqual compares functions only by nil-ness.
		return a.IsNil() && b.IsNil()
	}
	// The remaining kinds are comparable, and reflect reads them without
	// Interface, so this also works for an unexported field.
	return a.Equal(b)
}

// sameInstant compares two times by the instant JSON keeps. A time inside an
// unexported field cannot be read through reflect, so it is compared with Go's
// == instead: JSON never encodes that field, so nothing about it may change.
func sameInstant(a, b reflect.Value) bool {
	if !a.CanInterface() {
		return a.Equal(b)
	}
	return a.Interface().(time.Time).Equal(b.Interface().(time.Time))
}

// equalThrough compares the pair a pointer or an interface refers to.
func equalThrough(a, b reflect.Value, seen map[visit]bool) bool {
	if a.IsNil() || b.IsNil() {
		return a.IsNil() == b.IsNil()
	}
	if a.Kind() == reflect.Pointer {
		k, ok := enter(seen, a)
		if !ok {
			return true
		}
		defer delete(seen, k)
	}
	return equalRoundTripValue(a.Elem(), b.Elem(), seen)
}

// equalFields compares a struct's fields in order.
func equalFields(a, b reflect.Value, seen map[visit]bool) bool {
	for i := 0; i < a.NumField(); i++ {
		if !equalRoundTripValue(a.Field(i), b.Field(i), seen) {
			return false
		}
	}
	return true
}

// equalElements compares an array's elements in order.
func equalElements(a, b reflect.Value, seen map[visit]bool) bool {
	for i := 0; i < a.Len(); i++ {
		if !equalRoundTripValue(a.Index(i), b.Index(i), seen) {
			return false
		}
	}
	return true
}

func equalSlice(a, b reflect.Value, seen map[visit]bool) bool {
	if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
		return false
	}
	k, ok := enter(seen, a)
	if !ok {
		return true
	}
	defer delete(seen, k)
	return equalElements(a, b, seen)
}

func equalMap(a, b reflect.Value, seen map[visit]bool) bool {
	if a.IsNil() != b.IsNil() || a.Len() != b.Len() {
		return false
	}
	k, ok := enter(seen, a)
	if !ok {
		return true
	}
	defer delete(seen, k)
	iter := a.MapRange()
	for iter.Next() {
		value := b.MapIndex(iter.Key())
		if !value.IsValid() || !equalRoundTripValue(iter.Value(), value, seen) {
			return false
		}
	}
	return true
}
