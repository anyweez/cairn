package main

import (
	"fmt"
	"go/types"
	"reflect"
	"strings"
	"unicode"
)

// fields renders the JSON shape of a registered type, one entry per field, as
// encoding/json writes it. A type that isn't a struct is one entry, "=".
//
// codec.Encode marshals the registered value itself, which is not
// addressable, so a marshaler with a pointer receiver on that type is not
// used and its fields are what is stored.
func fields(t types.Type) []string {
	s := &shaper{
		active:    map[types.Type]bool{},
		embedding: map[*types.Named]bool{},
	}
	if st, ok := t.Underlying().(*types.Struct); ok && !s.opaque(t, false) {
		// Seed the type itself as being flattened, so an untagged embedded
		// pointer back to it is visited once and skipped the way
		// encoding/json treats a recursive type. This is embedding, not
		// active: active ends a named field at its name, embedding flattens.
		if named, ok := t.(*types.Named); ok {
			s.embedding[named] = true
			defer delete(s.embedding, named)
		}
		return s.structFields(st, false)
	}
	return []string{"=: " + s.shape(t, false)}
}

type shaper struct {
	// active holds the named types being rendered, so a recursive type ends
	// at its name instead of looping.
	active map[types.Type]bool
	// embedding holds the named types currently being flattened through
	// untagged embedded fields. A type that embeds a pointer to itself would
	// otherwise flatten forever; encoding/json visits it once and skips it.
	// It is separate from active because an embedded field is flattened
	// rather than named, so the active guard does not apply.
	embedding map[*types.Named]bool
}

// shape renders a type as JSON sees it: a named string type is a string, a
// struct is its fields, and a type that marshals itself is its name. addr
// says whether encoding/json reaches the value addressably (behind a pointer
// or in a slice), which decides whether pointer-receiver marshalers apply.
func (s *shaper) shape(t types.Type, addr bool) string {
	// An alias denotes the same type as its target, and Go models `type A = T`
	// as a *types.Alias rather than the *types.Named that every guard below
	// (and TypeString) understands. Resolve it first so an alias cannot slip
	// past the recursion guard.
	t = types.Unalias(t)
	if s.opaque(t, addr) {
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
		return "*" + s.shape(u.Elem(), true)
	case *types.Slice:
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte {
			return "bytes" // base64
		}
		return "[]" + s.shape(u.Elem(), true)
	case *types.Array:
		return fmt.Sprintf("[%d]%s", u.Len(), s.shape(u.Elem(), addr))
	case *types.Map:
		return "map[" + s.shape(u.Key(), false) + "]" + s.shape(u.Elem(), false)
	case *types.Struct:
		// A named struct rendered here is flattened into an object, exactly
		// as fields() flattens the top-level type. Mark it for the duration so
		// an embedded pointer back to it is visited once and skipped, as
		// encoding/json does; otherwise the outer struct's own fields are
		// emitted again after the embedded recursion returns.
		if named, ok := t.(*types.Named); ok && !s.embedding[named] {
			s.embedding[named] = true
			defer delete(s.embedding, named)
		}
		return "{" + strings.Join(s.structFields(u, addr), ", ") + "}"
	case *types.Interface:
		return "any"
	}
	return types.TypeString(t, nil)
}

// opaque reports whether t chooses its own JSON (time.Time, json.RawMessage),
// so its fields say nothing about what is stored. A marshaler with a pointer
// receiver counts only where the value is addressable.
//
// A method is only a marshaler if it has the signature encoding/json requires:
// no arguments and ([]byte, error) results. A method of the right name but the
// wrong signature is not used by encoding/json, which writes the fields
// instead, so calling the type opaque would hide a field-shape change from the
// -base comparison.
func (s *shaper) opaque(t types.Type, addr bool) bool {
	candidates := []types.Type{t}
	if addr {
		candidates = append(candidates, types.NewPointer(t))
	}
	for _, typ := range candidates {
		for _, name := range []string{"MarshalJSON", "MarshalText"} {
			obj, _, _ := types.LookupFieldOrMethod(typ, false, nil, name)
			fn, isFunc := obj.(*types.Func)
			if !isFunc {
				continue
			}
			if sig, ok := fn.Type().(*types.Signature); ok && isMarshalerSignature(sig) {
				return true
			}
		}
	}
	return false
}

// isMarshalerSignature reports whether sig satisfies json.Marshaler or
// encoding.TextMarshaler: no parameters and exactly ([]byte, error) results.
// The byte slice must be []byte itself; a named slice such as json.RawMessage
// does not satisfy the interface, and encoding/json writes the fields instead.
func isMarshalerSignature(sig *types.Signature) bool {
	if sig.Params().Len() != 0 || sig.Results().Len() != 2 {
		return false
	}
	if !types.Identical(sig.Results().At(0).Type(), types.NewSlice(types.Typ[types.Byte])) {
		return false
	}
	return types.Identical(sig.Results().At(1).Type(), types.Universe.Lookup("error").Type())
}

// structFields follows encoding/json: exported fields only, renamed or
// skipped by their json tag, with untagged embedded structs flattened.
func (s *shaper) structFields(st *types.Struct, addr bool) []string {
	var out []string
	for i := range st.NumFields() {
		f := st.Field(i)
		name, opts, tagged := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ",")
		if name == "-" && !tagged {
			continue
		}
		// encoding/json discards a tag name outside the tag alphabet and
		// silently falls back to the Go field name. Mirror that here, so the
		// manifest records the key encoding/json actually writes.
		if !isValidTag(name) {
			name = ""
		}
		if f.Embedded() && name == "" {
			// Resolve an alias to its target before the flattening guard
			// below looks for a *types.Named; `type A = T` embeds T's fields.
			typ, inner := types.Unalias(f.Type()), addr
			if ptr, ok := typ.(*types.Pointer); ok {
				typ, inner = types.Unalias(ptr.Elem()), true
			}
			if emb, ok := typ.Underlying().(*types.Struct); ok && !s.opaque(typ, inner) {
				named, _ := typ.(*types.Named)
				if named != nil {
					if s.embedding[named] {
						continue // an embedded type already being flattened: json skips it
					}
					s.embedding[named] = true
				}
				out = append(out, s.structFields(emb, inner)...)
				if named != nil {
					delete(s.embedding, named)
				}
				continue
			}
		}
		if !f.Exported() {
			// encoding/json ignores unexported fields, but keeps an anonymous
			// field whose type is a struct; such a field reaches here only when
			// its tag names it (an untagged embedded struct was flattened
			// above), and encoding/json marshals it as a named object.
			typ := f.Type()
			if ptr, ok := typ.(*types.Pointer); ok {
				typ = ptr.Elem()
			}
			if _, ok := typ.Underlying().(*types.Struct); !ok || !f.Embedded() || name == "" {
				continue
			}
		}
		if name == "" {
			name = f.Name()
		}
		if hasOption(opts, "omitempty") || hasOption(opts, "omitzero") {
			name += "?"
		}
		shape := s.shape(f.Type(), addr)
		if hasOption(opts, "string") && quotable(f.Type()) {
			shape = "string(" + shape + ")" // the value is written inside a JSON string
		}
		out = append(out, name+": "+shape)
	}
	return out
}

// isValidTag reports whether s may name a JSON field. It mirrors
// encoding/json's isValidTag: letters, digits and a fixed set of punctuation.
// A name containing a quote, a backslash or a control character is rejected
// and encoding/json falls back to the Go field name.
func isValidTag(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
			// Backslash and quote are reserved; other punctuation is allowed.
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}

func hasOption(opts, name string) bool {
	return strings.Contains(","+opts+",", ","+name+",")
}

// quotable reports whether the json ",string" option applies to a field of
// type t: a string, number or bool, or an unnamed pointer to one. Aliases are
// resolved first, because `type P = *int` is a *types.Alias whose underlying
// type is a pointer, not the *types.Pointer that reflect erases it to.
func quotable(t types.Type) bool {
	t = types.Unalias(t)
	if ptr, ok := t.(*types.Pointer); ok {
		t = types.Unalias(ptr.Elem())
	}
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsBoolean|types.IsNumeric|types.IsString) != 0
}
