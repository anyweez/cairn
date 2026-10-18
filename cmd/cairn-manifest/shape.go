package main

import (
	"cmp"
	"fmt"
	"go/types"
	"reflect"
	"slices"
	"strings"
	"unicode"
)

// fields renders the JSON shape of a registered type, one entry per field, as
// encoding/json writes it. A type that isn't a struct is one entry, "=".
//
// An omitempty or omitzero field is marked with "?" after its name. A name
// that itself contains "?" is escaped by doubling it, so a literal "?" is
// never mistaken for the marker.
//
// codec.Encode marshals the registered value itself, which is not
// addressable, so a marshaler with a pointer receiver on that type is not
// used and its fields are what is stored.
func fields(t types.Type) []string {
	s := &shaper{active: map[types.Type]bool{}}
	if _, ok := t.Underlying().(*types.Struct); ok && !s.opaque(t, false) {
		return s.structFields(t, false)
	}
	return []string{"=: " + s.shape(t, false)}
}

type shaper struct {
	// active holds the named types being rendered, so a recursive type ends
	// at its name instead of looping.
	active map[types.Type]bool
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
	// encoding/json special-cases json.Number, writing it as a bare number
	// rather than the quoted string its underlying kind would suggest. It is
	// the only basic kind it rewrites this way, so a change between
	// json.Number and string is a wire change the manifest must show.
	if isNamed(t, "encoding/json", "Number") {
		return "number"
	}
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
		// encoding/json applies the byte-slice base64 special case only when
		// the element type does not choose its own encoding. A named byte
		// type with MarshalJSON or MarshalText is written as its marshaled
		// value, so a slice of it is an array, not base64.
		if b, ok := u.Elem().Underlying().(*types.Basic); ok && b.Kind() == types.Byte && !s.opaque(u.Elem(), true) {
			return "bytes" // base64
		}
		return "[]" + s.shape(u.Elem(), true)
	case *types.Array:
		return fmt.Sprintf("[%d]%s", u.Len(), s.shape(u.Elem(), addr))
	case *types.Map:
		// encoding/json writes every string-kind map key as a quoted JSON
		// string, json.Number included, so a number key and a string key are
		// the same on the wire even though the same type in a value position
		// is a bare number. Render the key as a string rather than letting
		// the json.Number check above reach it.
		key := s.shape(u.Key(), false)
		if isNamed(types.Unalias(u.Key()), "encoding/json", "Number") {
			key = "string"
		}
		return "map[" + key + "]" + s.shape(u.Elem(), false)
	case *types.Struct:
		// The walk below keys its visited set on the named type, so pass t
		// rather than the bare struct: a recursive embedded pointer back to
		// this type must be recognised as one type and skipped, the way
		// encoding/json visits it once.
		return "{" + strings.Join(s.structFields(t, addr), ", ") + "}"
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

// jsonField is one field encoding/json may write, before name dominance
// removes the shadowed ones.
type jsonField struct {
	name   string // the JSON key
	index  []int  // path from the root struct: its depth and output order
	tagged bool   // the JSON tag named it, which breaks a depth tie
	entry  string // "name: shape", with "?" after an omitempty name
}

// structFields follows encoding/json: exported fields only, renamed or
// skipped by their json tag, with untagged embedded structs flattened. A JSON
// name reachable through more than one field is resolved as encoding/json
// resolves it: the shallowest field wins, a tagged field beats an untagged one
// at the same depth, and a remaining tie drops the name. It is a breadth-first
// walk so each field's depth is known, mirroring encoding/json's typeFields.
//
// The visited set is what ends the walk on a type that embeds a pointer to
// itself: encoding/json visits such a type once and skips the embedded field on
// every later visit, and keying the set on the resolved type does the same.
func (s *shaper) structFields(t types.Type, addr bool) []string {
	type queued struct {
		typ   types.Type // a struct reachable by embedding
		index []int      // path from the root struct
		addr  bool       // whether encoding/json reaches its fields addressably
	}

	current := []queued{}
	// The root of the walk carries the caller's addressability, so a direct
	// field of an addressable struct is shaped as encoding/json reaches it.
	next := []queued{{typ: t, addr: addr}}
	// count is how many times a type was queued at the current depth. A type
	// reachable twice contributes its fields twice, which is what makes
	// encoding/json drop an ambiguous name rather than pick one.
	var count, nextCount map[types.Type]int
	visited := map[types.Type]bool{}
	var cands []jsonField

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[types.Type]int{}
		for _, q := range current {
			if visited[q.typ] {
				continue
			}
			visited[q.typ] = true
			st, ok := q.typ.Underlying().(*types.Struct)
			if !ok {
				continue
			}
			for i := range st.NumFields() {
				f := st.Field(i)
				name, opts, hasOpts := strings.Cut(reflect.StructTag(st.Tag(i)).Get("json"), ",")
				if name == "-" && !hasOpts {
					continue
				}
				// encoding/json discards a tag name outside the tag alphabet and
				// silently falls back to the Go field name. Mirror that here, so
				// the manifest records the key encoding/json actually writes.
				if !isValidTag(name) {
					name = ""
				}
				tagged := name != ""

				index := make([]int, len(q.index)+1)
				copy(index, q.index)
				index[len(q.index)] = i

				// Resolve an alias to its target before the flattening guard
				// below looks for a struct, tests the opaque marshaler, or keys
				// the walk by type: `type A = T` embeds T's fields, and an alias
				// and its target must be one type or a recursive alias expands
				// forever as two.
				typ, inner := types.Unalias(f.Type()), q.addr
				if ptr, ok := typ.(*types.Pointer); ok {
					typ, inner = types.Unalias(ptr.Elem()), true
				}
				if f.Embedded() && name == "" {
					if _, ok := typ.Underlying().(*types.Struct); ok && !s.opaque(typ, inner) {
						nextCount[typ]++
						if nextCount[typ] == 1 {
							next = append(next, queued{typ: typ, index: index, addr: inner})
						}
						continue
					}
				}
				if !f.Exported() {
					// encoding/json ignores unexported fields, but keeps an
					// anonymous field whose type is a struct; such a field
					// reaches here only when its tag names it (an untagged
					// embedded struct was flattened above), and encoding/json
					// marshals it as a named object.
					innerType := f.Type()
					if ptr, ok := innerType.(*types.Pointer); ok {
						innerType = ptr.Elem()
					}
					if _, ok := innerType.Underlying().(*types.Struct); !ok || !f.Embedded() || name == "" {
						continue
					}
				}
				display := name
				if display == "" {
					display = f.Name()
				}
				// "?" after the name marks omitempty/omitzero, but "?" is also
				// a legal JSON key character, so a field tagged `json:"a?"`
				// would render exactly like `json:"a,omitempty"`: different
				// wire formats, one manifest entry. Double a literal "?" in
				// the rendered entry so the marker is the only single "?"; the
				// dominance key stays the raw JSON name encoding/json compares.
				entry := strings.ReplaceAll(display, "?", "??")
				if hasOption(opts, "omitempty") || hasOption(opts, "omitzero") {
					entry += "?"
				}
				shape := s.shape(f.Type(), q.addr)
				if hasOption(opts, "string") && quotable(f.Type()) {
					shape = "string(" + shape + ")" // written inside a JSON string
				}
				c := jsonField{name: display, index: index, tagged: tagged, entry: entry + ": " + shape}
				cands = append(cands, c)
				if count[q.typ] > 1 {
					// The name is reachable twice at this depth, so give the
					// dominance pass two entries and let it drop the tie.
					cands = append(cands, c)
				}
			}
		}
	}

	slices.SortFunc(cands, func(a, b jsonField) int {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c
		}
		if c := cmp.Compare(len(a.index), len(b.index)); c != 0 {
			return c
		}
		if a.tagged != b.tagged {
			if a.tagged {
				return -1
			}
			return 1
		}
		return slices.Compare(a.index, b.index)
	})

	out := cands[:0]
	for advance, i := 0, 0; i < len(cands); i += advance {
		name := cands[i].name
		for advance = 1; i+advance < len(cands) && cands[i+advance].name == name; advance++ {
		}
		if advance == 1 {
			out = append(out, cands[i])
			continue
		}
		if dominant, ok := dominantField(cands[i : i+advance]); ok {
			out = append(out, dominant)
		}
	}
	cands = out

	slices.SortFunc(cands, func(a, b jsonField) int { return slices.Compare(a.index, b.index) })
	fields := make([]string, len(cands))
	for i, c := range cands {
		fields[i] = c.entry
	}
	return fields
}

// dominantField picks the one field of a name that survives Go's embedding
// rules, modified by JSON tags. The candidates are sorted by depth and then by
// tag presence, so the first is the shallowest. Two at the same depth, both
// tagged or both untagged, are ambiguous and none survives.
func dominantField(fields []jsonField) (jsonField, bool) {
	if len(fields) > 1 && len(fields[0].index) == len(fields[1].index) && fields[0].tagged == fields[1].tagged {
		return jsonField{}, false
	}
	return fields[0], true
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
