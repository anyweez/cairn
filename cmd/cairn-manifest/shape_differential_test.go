package main

// A differential check on the field layout: for every fixture type below, a
// populated value is marshaled with encoding/json and compared with what
// fields() renders — every key must be named, every rendered name must be
// written, and the JSON kind of each value must match its shape. It is the
// check t2455 asked for: shape.go reimplements encoding/json's field rules by
// hand, and one divergence at a time is how they were found.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode"
)

// DiffEmbeddedA and DiffEmbeddedB each marshal themselves with a value
// receiver. Embedded together, the promoted methods collide and cancel, so
// the outer type has no marshaler and encoding/json writes the embedded
// fields flattened: typeFields flattens an embedded struct regardless of the
// embedded type's own marshaler.
type DiffEmbeddedA struct {
	AX int `json:"ax"`
}

func (DiffEmbeddedA) MarshalJSON() ([]byte, error) { return []byte(`"a"`), nil }

type DiffEmbeddedB struct {
	BX int `json:"bx"`
}

func (DiffEmbeddedB) MarshalJSON() ([]byte, error) { return []byte(`"b"`), nil }

type DiffAmbiguous struct {
	DiffEmbeddedA
	DiffEmbeddedB
}

// An embedded struct whose marshaler has a pointer receiver. The outer type
// does not implement MarshalJSON (the method is not in its value method
// set), and at a non-addressable root encoding/json reaches the embedded
// value as a value, so the fields are written.
type DiffAddrInner struct {
	Kept int `json:"kept"`
}

func (*DiffAddrInner) MarshalJSON() ([]byte, error) { return []byte(`"inner"`), nil }

type DiffAddrHolder struct {
	DiffAddrInner
}

type DiffAddrOuter struct {
	H DiffAddrHolder `json:"h"`
}

// Dominance: the outer field's shallow "x" beats the embedded one.
type DiffPlain struct {
	X int `json:"x"`
}

type DiffShadow struct {
	X string `json:"x"`
	DiffPlain
}

// A same-depth tie of one name, which encoding/json drops entirely, cannot
// be declared in vet-checked source (two fields repeating a json tag at the
// same level is itself a vet error); TestFieldDominanceDropsATieAndPrefersATag
// builds it with go/types for that reason.

// An embedded pointer to a struct is flattened through the pointer.
type DiffPtrEmbedded struct {
	*DiffPlain
}

// An embedded unexported alias of a pointer to a struct, with a JSON tag:
// encoding/json writes the pointed-to struct as a named object even though the
// field is unexported, so a change to the nested struct is a wire change the
// manifest has to show.
type aliasPointerMetaV1 struct {
	Old string `json:"old"`
}

type aliasPointerMetaV2 struct {
	New string `json:"new"`
}

type aliasPointerV1 = *aliasPointerMetaV1
type aliasPointerV2 = *aliasPointerMetaV2

type aliasPointerOuterV1 struct {
	aliasPointerV1 `json:"meta"`
	Y              int `json:"y"`
}

type aliasPointerOuterV2 struct {
	aliasPointerV2 `json:"meta"`
	Y              int `json:"y"`
}

// The same declaration without an alias: an embedded unexported pointer to a
// struct, with a JSON tag.
type diffUnexportedMeta struct {
	Old string `json:"old"`
}

type DiffUnexportedPtrEmbedded struct {
	*diffUnexportedMeta `json:"meta"`
	Y                   int `json:"y"`
}

// A json.Number field is a bare number, not the string its kind suggests.
type DiffNumber struct {
	N json.Number `json:"n"`
}

// A json.Number field under ",string" is still a bare number.
type DiffNumberQuoted struct {
	N json.Number `json:"n,string"`
}

// A string-kind map key never has its MarshalText called.
type DiffKey string

func (DiffKey) MarshalText() ([]byte, error) { return []byte("k"), nil }

type DiffMapKey struct {
	M map[DiffKey]int `json:"m"`
}

// A named byte type with its own marshaler is an array, not base64.
type DiffByte byte

func (DiffByte) MarshalJSON() ([]byte, error) { return []byte("7"), nil }

type DiffNamedBytes struct {
	B []DiffByte `json:"b"`
}

// json.RawMessage writes what it holds, not a base64 string.
type DiffRaw struct {
	R json.RawMessage `json:"r"`
}

// The markers: omitempty and omitzero fields are still written when
// populated, and a tagged embedded struct with a marshaler is written as its
// marshaled value under the tag's name.
type DiffOmit struct {
	A int `json:"a,omitempty"`
	B int `json:"b,omitzero"`
}

type DiffTaggedEmbedded struct {
	A DiffEmbeddedA `json:"a"`
}

// A ",string" option on a type with its own marshaler is ignored.
type DiffMarshalerStr string

func (DiffMarshalerStr) MarshalText() ([]byte, error) { return []byte("Ts"), nil }

type DiffQuotedMarshaler struct {
	F DiffMarshalerStr `json:"f,string"`
}

// Everything at once, so the kinds of the basic shapes stay checked.
type DiffKitchen struct {
	S  string         `json:"s"`
	I  int            `json:"i"`
	U  uint8          `json:"u"`
	F  float64        `json:"f"`
	B  bool           `json:"b"`
	M  map[string]int `json:"m"`
	L  []string       `json:"l"`
	P  *int           `json:"p"`
	An any            `json:"an"`
}

// diffFixtures are populated by reflection and marshaled, so the harness
// stays independent of how each type is filled.
var diffFixtures = []any{
	DiffAmbiguous{},
	DiffAddrOuter{},
	DiffShadow{},
	DiffPtrEmbedded{},
	DiffUnexportedPtrEmbedded{},
	aliasPointerOuterV1{},
	aliasPointerOuterV2{},
	DiffNumber{},
	DiffNumberQuoted{},
	DiffMapKey{},
	DiffNamedBytes{},
	DiffRaw{},
	DiffOmit{},
	DiffTaggedEmbedded{},
	DiffQuotedMarshaler{},
	DiffKitchen{},
}

// TestFieldsMatchesEncodingJSONForEveryFixture compares fields() against real
// encoding/json output for every fixture type: a populated value of the type
// is marshaled, and every key encoding/json writes must be a name fields()
// rendered, every rendered name must be written, and where the shape says
// which kind of JSON value it is, the written value must be that kind.
func TestFieldsMatchesEncodingJSONForEveryFixture(t *testing.T) {
	declared := loadDeclaredTypes(t)
	for _, fixture := range diffFixtures {
		rt := reflect.TypeOf(fixture)
		name := rt.Name()
		t.Run(name, func(t *testing.T) {
			tt, ok := declared[name]
			if !ok {
				t.Fatalf("%s was not found in the loaded package", name)
			}

			// Populate a fresh value of the type so no field is empty, then
			// marshal it the way codec.Encode would: a plain, non-addressable
			// value.
			val := reflect.New(rt).Elem()
			populate(val, 0)
			raw, err := json.Marshal(val.Interface())
			if err != nil {
				t.Fatalf("encoding/json could not marshal %s: %v", name, err)
			}
			var written map[string]json.RawMessage
			if err := json.Unmarshal(raw, &written); err != nil {
				t.Fatalf("encoding/json wrote %s, not an object, so the fixture does not fit this check", raw)
			}

			entries := fields(tt)
			rendered := map[string]bool{}
			for _, entry := range entries {
				key, shape := splitEntry(entry)
				if rendered[key] {
					t.Errorf("fields() renders %q twice: %q", key, entries)
					continue
				}
				rendered[key] = true
				if raw, ok := written[key]; !ok {
					t.Errorf("fields() names %q but encoding/json writes %s", entry, raw)
				} else if kind := jsonKind(shape); kind != "" {
					if got := jsonRawKind(raw); got != kind && !populateLeavesItNil(rt, key, shape, got) {
						t.Errorf("fields() renders %q, %s; encoding/json writes %s, %s", entry, kind, raw, got)
					}
				}
			}
			for key := range written {
				if !rendered[key] {
					t.Errorf("encoding/json writes %q but fields() does not name it: %q", key, entries)
				}
			}
		})
	}
}

// populateLeavesItNil reports whether the null encoding/json wrote for a field
// is the harness's own doing rather than a divergence: reflection cannot set
// an unexported field, so an unexported embedded pointer stays nil and
// encoding/json writes null for it, however the shape describes what it points
// at. Only that exact case is excused, so a pointer the harness populated is
// still held to the kind its shape names.
func populateLeavesItNil(rt reflect.Type, key, shape, got string) bool {
	return got == "null" && strings.HasPrefix(shape, "*") && unexportedField(rt, key)
}

// unexportedField reports whether rt declares a field that the JSON key names
// and reflection cannot set. Untagged embedded structs are searched through,
// as their fields are promoted; a field behind an unexported field is not,
// since populate never reaches it either.
func unexportedField(rt reflect.Type, key string) bool {
	for i := range rt.NumField() {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && unexportedField(ft, key) {
				return true
			}
			continue
		}
		if name == key && !f.IsExported() {
			return true
		}
	}
	return false
}

// splitEntry splits a rendered entry into its JSON key and its shape. A
// trailing "?" is the omitempty marker, which a literal "?" escapes by
// doubling, so the marker is stripped before the doubling is undone.
func splitEntry(entry string) (name, shape string) {
	name, shape, _ = strings.Cut(entry, ": ")
	if strings.HasSuffix(name, "?") && !strings.HasSuffix(name, "??") {
		name = name[:len(name)-1]
	}
	return strings.ReplaceAll(name, "??", "?"), shape
}

// jsonKind maps the shape fields() renders to the kind of JSON value
// encoding/json writes for it: "" when the shape is a named type whose
// marshaler output could be anything, so only its key can be checked.
func jsonKind(shape string) string {
	switch {
	case strings.HasPrefix(shape, "*"):
		return jsonKind(shape[1:])
	case strings.HasPrefix(shape, "[]"):
		return "array"
	case strings.HasPrefix(shape, "map["), strings.HasPrefix(shape, "{"):
		return "object"
	case strings.HasPrefix(shape, "string("):
		return "string"
	}
	name := shape
	if i := strings.IndexFunc(shape, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}); i >= 0 {
		name = shape[:i]
	}
	switch name {
	case "string", "bytes":
		return "string"
	case "bool":
		return "bool"
	case "number", "int", "int8", "int16", "int32", "int64", "uint", "uint8",
		"uint16", "uint32", "uint64", "uintptr", "byte", "rune", "float32", "float64":
		return "number"
	case "any":
		return "null"
	}
	return ""
}

// jsonRawKind reads the kind of a value encoding/json wrote.
func jsonRawKind(raw json.RawMessage) string {
	switch raw[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	}
	return "number"
}

// populate fills v with non-zero values, so marshaling it writes every field
// encoding/json would write. Unexported fields stay zero, as reflection
// cannot set them and encoding/json does not write them. Interface values
// stay nil, so they are written as null.
func populate(v reflect.Value, depth int) {
	if depth > 3 {
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		for i := range v.NumField() {
			if f := v.Field(i); f.CanSet() {
				populate(f, depth+1)
			}
		}
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		populate(v.Elem(), depth+1)
	case reflect.Slice:
		// json.RawMessage must hold valid JSON or marshaling it is an error;
		// an object also exercises the "marshaler, not base64" path it takes.
		if v.Type() == reflect.TypeOf(json.RawMessage(nil)) {
			v.SetBytes([]byte(`{"k":1}`))
			return
		}
		s := reflect.MakeSlice(v.Type(), 1, 1)
		populate(s.Index(0), depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMapWithSize(v.Type(), 1)
		k := reflect.New(v.Type().Key()).Elem()
		if k.Kind() == reflect.String {
			k.SetString("k")
		} else {
			k.SetInt(1)
		}
		ev := reflect.New(v.Type().Elem()).Elem()
		populate(ev, depth+1)
		m.SetMapIndex(k, ev)
		v.Set(m)
	case reflect.String:
		// json.Number holds a number literal, and marshaling an invalid one
		// is an error rather than a value of the wrong kind.
		if v.Type() == reflect.TypeOf(json.Number("")) {
			v.SetString("1")
		} else {
			v.SetString("s")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	}
}
