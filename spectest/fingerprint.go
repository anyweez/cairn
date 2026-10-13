package spectest

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// fingerprint renders a value deeply — through pointers, maps and slices,
// including unexported fields — so a change anywhere it reaches shows up as a
// different string. It is how Isolated catches a Decide or Evolve that writes
// into a map or slice it shares with the state it was given.
func fingerprint(v any) string {
	var b strings.Builder
	walk(&b, reflect.ValueOf(v), map[visit]bool{})
	return b.String()
}

// visit identifies a pointer, map or slice on the current path, so a value
// that contains itself renders as <cycle> instead of recursing forever. A
// slice is only the same value at the same length.
type visit struct {
	ptr  uintptr
	kind reflect.Kind
	len  int
}

// enter marks v as on the path, or reports that it already is.
func enter(seen map[visit]bool, v reflect.Value) (visit, bool) {
	k := visit{ptr: v.Pointer(), kind: v.Kind()}
	if v.Kind() == reflect.Slice {
		k.len = v.Len()
	}
	if seen[k] {
		return k, false
	}
	seen[k] = true
	return k, true
}

func walk(b *strings.Builder, v reflect.Value, seen map[visit]bool) {
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
		b.WriteByte('&')
		walk(b, v.Elem(), seen)
		delete(seen, k)
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		fmt.Fprintf(b, "(%s)", v.Elem().Type())
		walk(b, v.Elem(), seen)
	case reflect.Struct:
		b.WriteByte('{')
		for i := range v.NumField() {
			fmt.Fprintf(b, "%s:", v.Type().Field(i).Name)
			walk(b, v.Field(i), seen)
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
			walk(b, v.Index(i), seen)
			b.WriteByte(',')
		}
		// A slice's spare capacity is memory a successor can share: appending
		// to the given state's slice writes there without changing its length.
		if v.Kind() == reflect.Slice && v.Cap() > v.Len() {
			b.WriteByte('|')
			spare := v.Slice(v.Len(), v.Cap())
			for i := range spare.Len() {
				walk(b, spare.Index(i), seen)
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
			walk(&kb, iter.Key(), seen)
			walk(&vb, iter.Value(), seen)
			entries = append(entries, kv{kb.String(), vb.String()})
		}
		// Distinct keys can render alike (two pointers to equal values), so
		// ties sort by value to keep the order independent of map iteration.
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
