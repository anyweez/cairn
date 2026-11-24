package spectest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// renderMode selects what a render must distinguish. A fingerprint answers
// whether the object graph changed, so it renders a pointer's identity and a
// slice's spare capacity. The D6/D7 failure detail answers what the round-trip
// comparison reads: it compares pointees and elements, which ignores both, and
// a time.Time by its instant rather than by its unexported fields.
type renderMode struct {
	identity bool
	instant  bool
}

// fingerprint renders a value deeply — through pointers, maps and slices,
// including unexported fields — so a change anywhere it reaches shows up as a
// different string. It is how Isolated catches a Decide or Evolve that writes
// into a map or slice it shares with the state it was given.
func fingerprint(v any) string {
	var b strings.Builder
	walk(&b, reflect.ValueOf(v), map[visit]bool{}, renderMode{identity: true})
	return b.String()
}

// visit identifies a pointer, map or slice on the current path, so a value
// that contains itself renders as <cycle> instead of recursing forever. A
// slice is only the same value at the same length. The type is part of the
// identity too: different objects can share an address — every zero-length
// slice points at zerobase, a struct and its first field share one, and two
// named map types can share one backing map — and treating them as the same
// object would make clone install one type's copy in another type's field.
type visit struct {
	ptr  uintptr
	kind reflect.Kind
	len  int
	typ  reflect.Type
}

// visitKey names the object v refers to, for the path maps fingerprint and
// clone keep.
func visitKey(v reflect.Value) visit {
	k := visit{ptr: v.Pointer(), kind: v.Kind(), typ: v.Type()}
	if v.Kind() == reflect.Slice {
		k.len = v.Len()
	}
	return k
}

// enter marks v as on the path, or reports that it already is.
func enter(seen map[visit]bool, v reflect.Value) (visit, bool) {
	k := visitKey(v)
	if seen[k] {
		return k, false
	}
	seen[k] = true
	return k, true
}

func walk(b *strings.Builder, v reflect.Value, seen map[visit]bool, mode renderMode) {
	if !v.IsValid() {
		b.WriteString("<nil>")
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		k, ok := enter(seen, v)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		// A pointer's identity is part of the value, not just its pointee: two
		// keys pointing at equal values are still different keys, and a map
		// that swaps their values is a different map. Without the address the
		// two keys render alike, the swap is invisible, and a D4 mutation
		// survives. A fingerprint is only ever compared for one object graph
		// within a run, so the address is stable across the comparison. A round
		// trip compares pointees, so its detail leaves the address out.
		if mode.identity {
			fmt.Fprintf(b, "&%x", v.Pointer())
		} else {
			b.WriteByte('&')
		}
		walk(b, v.Elem(), seen, mode)
		delete(seen, k)
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		fmt.Fprintf(b, "(%s)", v.Elem().Type())
		walk(b, v.Elem(), seen, mode)
	case reflect.Struct:
		// The comparison reads a time by its instant, and JSON keeps the
		// instant but neither the *Location nor the monotonic reading, so a
		// detail built from its fields would show differences the round trip
		// ignores. An unexported time is invisible to JSON and cannot change,
		// so it falls through to the fields it is.
		if mode.instant && v.Type() == timeType && v.CanInterface() {
			b.WriteString(v.Interface().(time.Time).UTC().Format(time.RFC3339Nano))
			return
		}
		b.WriteByte('{')
		for i := range v.NumField() {
			fmt.Fprintf(b, "%s:", v.Type().Field(i).Name)
			walk(b, v.Field(i), seen, mode)
			b.WriteByte(',')
		}
		b.WriteByte('}')
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice {
			if v.IsNil() {
				b.WriteString("nil")
				return
			}
			k, ok := enter(seen, v)
			if !ok {
				b.WriteString("<cycle>")
				return
			}
			defer delete(seen, k)
		}
		b.WriteByte('[')
		for i := range v.Len() {
			walk(b, v.Index(i), seen, mode)
			b.WriteByte(',')
		}
		// A slice's spare capacity is memory a successor can share: appending
		// to the given state's slice writes there without changing its length.
		if mode.identity && v.Kind() == reflect.Slice && v.Cap() > v.Len() {
			b.WriteByte('|')
			spare := v.Slice(v.Len(), v.Cap())
			for i := range spare.Len() {
				walk(b, spare.Index(i), seen, mode)
				b.WriteByte(',')
			}
		}
		b.WriteByte(']')
	case reflect.Map:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		k, ok := enter(seen, v)
		if !ok {
			b.WriteString("<cycle>")
			return
		}
		defer delete(seen, k)
		type kv struct{ k, v string }
		entries := make([]kv, 0, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			var kb, vb strings.Builder
			walk(&kb, iter.Key(), seen, mode)
			walk(&vb, iter.Value(), seen, mode)
			entries = append(entries, kv{kb.String(), vb.String()})
		}
		// Distinct keys can still render alike — no NaN equals another, so a
		// map can hold several keys that all render "NaN" — so ties sort by
		// value to keep the order independent of map iteration.
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].k != entries[j].k {
				return entries[i].k < entries[j].k
			}
			return entries[i].v < entries[j].v
		})
		b.WriteString("map[")
		for _, e := range entries {
			b.WriteString(e.k + ":" + e.v + ",")
		}
		b.WriteByte(']')
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		fmt.Fprintf(b, "%s@%x", v.Kind(), v.Pointer())
	case reflect.String:
		fmt.Fprintf(b, "%q", v.String())
	case reflect.Bool:
		fmt.Fprint(b, v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fmt.Fprint(b, v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		fmt.Fprint(b, v.Uint())
	case reflect.Float32, reflect.Float64:
		fmt.Fprint(b, v.Float())
	case reflect.Complex64, reflect.Complex128:
		fmt.Fprint(b, v.Complex())
	default:
		fmt.Fprintf(b, "<%s>", v.Kind())
	}
}
