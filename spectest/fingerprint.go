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
	walk(&b, reflect.ValueOf(v), map[uintptr]bool{})
	return b.String()
}

func walk(b *strings.Builder, v reflect.Value, seen map[uintptr]bool) {
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
		if seen[v.Pointer()] {
			b.WriteString("<cycle>")
			return
		}
		seen[v.Pointer()] = true
		b.WriteByte('&')
		walk(b, v.Elem(), seen)
		delete(seen, v.Pointer())
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
		if v.Kind() == reflect.Slice && v.IsNil() {
			b.WriteString("nil")
			return
		}
		b.WriteByte('[')
		for i := range v.Len() {
			walk(b, v.Index(i), seen)
			b.WriteByte(',')
		}
		b.WriteByte(']')
	case reflect.Map:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		type kv struct{ k, v string }
		entries := make([]kv, 0, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			var kb, vb strings.Builder
			walk(&kb, iter.Key(), seen)
			walk(&vb, iter.Value(), seen)
			entries = append(entries, kv{kb.String(), vb.String()})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].k < entries[j].k })
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
