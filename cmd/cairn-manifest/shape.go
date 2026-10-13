package main

import (
	"fmt"
	"go/types"
	"reflect"
	"strings"
)

// fields renders the JSON shape of a registered type, one entry per field, as
// encoding/json writes it. A type that isn't a struct is one entry, "=".
func fields(t types.Type) []string {
	s := &shaper{active: map[types.Type]bool{}}
	if st, ok := t.Underlying().(*types.Struct); ok && !s.opaque(t) {
		return s.structFields(st)
	}
	return []string{"=: " + s.shape(t)}
}

type shaper struct {
	// active holds the named types being rendered, so a recursive type ends
	// at its name instead of looping.
	active map[types.Type]bool
}

// shape renders a type as JSON sees it: a named string type is a string, a
// struct is its fields, and a type that marshals itself is its name.
func (s *shaper) shape(t types.Type) string {
	if s.opaque(t) {
		return types.TypeString(t, nil)
	}
	if named, ok := t.(*types.Named); ok {
		if s.active[named] {
			return types.TypeString(t, nil)
		}
		s.active[named] = true
		defer delete(s.active, named)
	}
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Name()
	case *types.Pointer:
		return "*" + s.shape(u.Elem())
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return "bytes" // base64
		}
		return "[]" + s.shape(u.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", u.Len(), s.shape(u.Elem()))
	case *types.Map:
		return "map[" + s.shape(u.Key()) + "]" + s.shape(u.Elem())
	case *types.Struct:
		return "{" + strings.Join(s.structFields(u), ", ") + "}"
	case *types.Interface:
		return "any"
	}
	return types.TypeString(t, nil)
}

// opaque reports whether t chooses its own JSON (time.Time, json.RawMessage),
// so its fields say nothing about what is stored.
func (s *shaper) opaque(t types.Type) bool {
	for _, name := range []string{"MarshalJSON", "MarshalText"} {
		for _, typ := range []types.Type{t, types.NewPointer(t)} {
			if obj, _, _ := types.LookupFieldOrMethod(typ, true, nil, name); obj != nil {
				if _, isFunc := obj.(*types.Func); isFunc {
					return true
				}
			}
		}
	}
	return false
}

// structFields follows encoding/json: exported fields only, renamed or
// skipped by their json tag, with untagged embedded structs flattened.
func (s *shaper) structFields(st *types.Struct) []string {
	var out []string
	for i := range st.NumFields() {
		f := st.Field(i)
		name, opts, tagged := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ",")
		if name == "-" && !tagged {
			continue
		}
		if f.Embedded() && name == "" {
			typ := f.Type()
			if ptr, ok := typ.(*types.Pointer); ok {
				typ = ptr.Elem()
			}
			if inner, ok := typ.Underlying().(*types.Struct); ok && !s.opaque(typ) {
				out = append(out, s.structFields(inner)...)
				continue
			}
		}
		if !f.Exported() {
			continue
		}
		if name == "" {
			name = f.Name()
		}
		if strings.Contains(","+opts+",", ",omitempty,") || strings.Contains(","+opts+",", ",omitzero,") {
			name += "?"
		}
		out = append(out, name+": "+s.shape(f.Type()))
	}
	return out
}
