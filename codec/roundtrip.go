package codec

import (
	"math"
	"reflect"
	"time"
	"unsafe"
)

// EqualRoundTripped reports whether a value and what its codec, snapshot or a
// replay kept it as are the same value.
//
// It is reflect.DeepEqual with three differences, each the notion of "read back
// unchanged" this package and the ledger hold themselves to. A time is compared
// by its instant and its zone offset, whether it is a time.Time, a type whose
// underlying type is time.Time, or a time reached through a field, pointer or
// interface: json.Marshal writes an instant and an offset but neither the
// monotonic clock reading nor the *Location it came from, so a time the ledger
// records, snapshots and replays exactly fails DeepEqual. The offset is part of
// the text RFC 3339 writes, so a time whose instant is kept but whose offset
// changes is a different stored value, not a surviving one. Two NaNs of the
// same float type are equal, because each run reached the same value and Go's
// == says a NaN differs from itself. Map keys are associated by this walk rather
// than by Go's ==, so a keyed-by time, NaN or pointer survives when the walk
// considers the key the same.
//
// CheckStorable is the ledger's rule for what may be written; this is that rule
// applied to a pair, held to the offsets the stored text carries. What the walk
// does not treat specially keeps DeepEqual's answer, so a field a codec or a
// snapshot drops — a `json:"-"` tag, an unexported field — is still reported,
// and so is a time whose instant moves, a NaN against a number, and a key that
// is genuinely gone.
func EqualRoundTripped(a, b any) bool {
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
	a, b = readable(a), readable(b)
	if a.Type() != b.Type() {
		return false
	}
	if x, ok := timeValue(a); ok {
		y, ok := timeValue(b)
		return ok && sameInstant(x, y)
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
	case reflect.Float32, reflect.Float64:
		// A NaN is not equal to itself in Go, but two runs that reached the
		// same NaN reached the same stored value.
		x, y := a.Float(), b.Float()
		return x == y || math.IsNaN(x) && math.IsNaN(y)
	}
	// The remaining kinds are comparable, and reflect reads them without
	// Interface, so this also works for an unexported field.
	return a.Equal(b)
}

// readable clears reflect's read-only flag on an addressable unexported field,
// so a time behind one can be read as the time.Time it is. The flag is on the
// Value, not the memory; an addressable field is safe to read, and the copy is
// not written.
func readable(v reflect.Value) reflect.Value {
	if v.IsValid() && !v.CanInterface() && v.CanAddr() {
		return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
	}
	return v
}

// addressable returns v as an addressable value, copying a struct or array that
// reflect reached through a map value or a non-addressable container. Its fields
// are then addressable, which is what lets readable read an unexported one.
func addressable(v reflect.Value) reflect.Value {
	v = readable(v)
	if v.CanAddr() {
		return v
	}
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	return out
}

// timeValue returns the time a value carries: a time.Time, or a type whose
// underlying type is time.Time, which JSON stores through time's own encoding
// when the type delegates to it. A struct embedding a time is reached by the
// field walk, which reads the embedded time itself.
func timeValue(v reflect.Value) (time.Time, bool) {
	if v.Type() != timeType && !v.Type().ConvertibleTo(timeType) {
		return time.Time{}, false
	}
	if !v.CanInterface() {
		return time.Time{}, false
	}
	return v.Convert(timeType).Interface().(time.Time), true
}

// sameInstant reports whether two times are the same value JSON keeps. JSON
// writes the instant and the zone offset, so both must be equal; the *Location
// name and the monotonic reading are dropped, so they may differ.
func sameInstant(x, y time.Time) bool {
	_, xOffset := x.Zone()
	_, yOffset := y.Zone()
	return x.Equal(y) && xOffset == yOffset
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
	a, b = addressable(a), addressable(b)
	for i := 0; i < a.NumField(); i++ {
		if !equalRoundTripValue(a.Field(i), b.Field(i), seen) {
			return false
		}
	}
	return true
}

// equalElements compares an array's elements in order.
func equalElements(a, b reflect.Value, seen map[visit]bool) bool {
	a, b = addressable(a), addressable(b)
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
	type entry struct{ key, value reflect.Value }
	var candidates []entry
	other := b.MapRange()
	for other.Next() {
		candidates = append(candidates, entry{key: other.Key(), value: other.Value()})
	}
	matched := make([]bool, len(candidates))
	iter := a.MapRange()
	for iter.Next() {
		found := false
		for i, candidate := range candidates {
			if matched[i] || !equalRoundTripValue(iter.Key(), candidate.key, seen) {
				continue
			}
			if equalRoundTripValue(iter.Value(), candidate.value, seen) {
				matched[i] = true
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
