// Package codec gives a sum type — an interface with a closed set of concrete
// types — a stable, versioned JSON encoding.
//
// Every type stored in the ledger is registered under a name that never
// changes and a version that goes up when its shape does. Decoding an old
// version runs it through the upcasters registered for that name, one version
// at a time, so Evolve only ever sees the current shape.
package codec

import (
	"bytes"
	"cmp"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/anyweez/cairn"
)

// Envelope is one encoded value as the ledger stores it.
type Envelope struct {
	Type    string          `json:"type"`
	Version int             `json:"v"`
	Data    json.RawMessage `json:"data"`
}

// Upcaster rewrites version n of a type's JSON into version n+1.
type Upcaster func(json.RawMessage) (json.RawMessage, error)

type entry struct {
	name    string
	version int
	typ     reflect.Type
	// upcasters[n] rewrites version n to n+1.
	upcasters map[int]Upcaster
}

// Codec encodes and decodes the values of one sum type T.
type Codec[T any] struct {
	byName map[string]*entry
	byType map[reflect.Type]*entry
}

// New returns an empty codec for T.
func New[T any]() *Codec[T] {
	return &Codec[T]{byName: map[string]*entry{}, byType: map[reflect.Type]*entry{}}
}

// Register adds V to c under name, at version (1 for a type that has never
// changed shape). V must implement T. Register panics on a duplicate name or
// type: registration happens once, at startup, and a mistake there is a bug.
func Register[V, T any](c *Codec[T], name string, version int) {
	var zero V
	if _, ok := any(zero).(T); !ok {
		panic(fmt.Sprintf("codec: %T does not implement %s", zero, reflect.TypeFor[T]()))
	}
	if version < 1 {
		panic(fmt.Sprintf("codec: %s: version must be at least 1", name))
	}
	typ := reflect.TypeFor[V]()
	if _, dup := c.byName[name]; dup {
		panic(fmt.Sprintf("codec: %s registered twice", name))
	}
	if _, dup := c.byType[typ]; dup {
		panic(fmt.Sprintf("codec: %s registered twice", typ))
	}
	e := &entry{name: name, version: version, typ: typ, upcasters: map[int]Upcaster{}}
	c.byName[name] = e
	c.byType[typ] = e
}

// Upcast registers how version from of name becomes version from+1.
func (c *Codec[T]) Upcast(name string, from int, f Upcaster) {
	e, ok := c.byName[name]
	if !ok {
		panic(fmt.Sprintf("codec: upcaster for unregistered type %s", name))
	}
	if from >= e.version {
		panic(fmt.Sprintf("codec: %s is at version %d; an upcaster from %d has nothing to produce", name, e.version, from))
	}
	e.upcasters[from] = f
}

// Encode wraps v in an envelope naming its registered type and version.
//
// It refuses a value carrying text the ledger cannot store rather than
// letting json.Marshal quietly replace it with U+FFFD: a codec that silently
// rewrites a message is one whose entries no longer say what the caller sent.
func (c *Codec[T]) Encode(v T) (Envelope, error) {
	typ := reflect.TypeOf(any(v))
	e, ok := c.byType[typ]
	if !ok {
		return Envelope{}, fmt.Errorf("codec: %v is not registered", typ)
	}
	if err := CheckStorable(v); err != nil {
		return Envelope{}, fmt.Errorf("codec: encoding %s: %w", e.name, err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return Envelope{}, fmt.Errorf("codec: encoding %s: %w", e.name, err)
	}
	return Envelope{Type: e.name, Version: e.version, Data: data}, nil
}

// NotStorableError names the field of a value that carries text the ledger
// cannot store. It is returned by Encode and reported by CheckStorable.
type NotStorableError struct {
	Path string
}

func (e *NotStorableError) Error() string {
	return fmt.Sprintf("%s is not valid UTF-8 or contains a NUL, so it cannot be stored", e.Path)
}

// CheckStorable reports whether every string in v can be stored in the ledger
// and read back unchanged. json.Marshal replaces a string that is not valid
// UTF-8 with U+FFFD, and Postgres jsonb cannot represent a NUL, so either
// would silently store text the caller never wrote.
//
// It walks the same values json.Marshal does. A type with its own MarshalJSON
// decides its own encoding and is not inspected, because those bytes are the
// JSON. A type with its own MarshalText does not: json writes the text through
// its own string encoder, so the bytes it returns are checked like any other
// string.
func CheckStorable(v any) error {
	return checkStorable(reflect.ValueOf(v), "value", map[visit]bool{})
}

// visit identifies a pointer, map or slice on the walk's current path, so a
// value that contains itself is not followed forever. json.Marshal rejects a
// cycle when it encodes the value, so the walk may treat one as storable and
// leave the error to Encode's marshal.
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

func checkStorable(v reflect.Value, path string, seen map[visit]bool) error {
	if !v.IsValid() {
		return nil
	}
	if v.CanInterface() {
		if _, ok := v.Interface().(json.Marshaler); ok {
			return nil
		}
		if _, ok := v.Interface().(encoding.TextMarshaler); ok {
			// json.Marshal renders a nil pointer as null and never calls
			// MarshalText.
			if v.Kind() == reflect.Pointer && v.IsNil() {
				return nil
			}
			_, err := renderStorableText(v, path)
			return err
		}
	}
	switch v.Kind() {
	case reflect.String:
		if !cairn.StorableText(v.String()) {
			return &NotStorableError{Path: path}
		}
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return checkStorable(v.Elem(), path, seen)
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		k, ok := enter(seen, v)
		if !ok {
			return nil
		}
		defer delete(seen, k)
		return checkStorable(v.Elem(), path, seen)
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			f := t.Field(i)
			// json.Marshal skips an unexported field, but not an unexported
			// embedded struct's exported fields.
			if f.PkgPath != "" && !f.Anonymous {
				continue
			}
			if f.Tag.Get("json") == "-" {
				continue
			}
			if err := checkStorable(v.Field(i), path+"."+f.Name, seen); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		k, ok := enter(seen, v)
		if !ok {
			return nil
		}
		defer delete(seen, k)
		for i := 0; i < v.Len(); i++ {
			if err := checkStorable(v.Index(i), fmt.Sprintf("%s[%d]", path, i), seen); err != nil {
				return err
			}
		}
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := checkStorable(v.Index(i), fmt.Sprintf("%s[%d]", path, i), seen); err != nil {
				return err
			}
		}
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		k, ok := enter(seen, v)
		if !ok {
			return nil
		}
		defer delete(seen, k)
		iter := v.MapRange()
		for iter.Next() {
			key, err := checkMapKey(iter.Key(), path+" key")
			if err != nil {
				return err
			}
			if err := checkStorable(iter.Value(), path+"["+key+"]", seen); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkMapKey checks a map key the way json.Marshal will render it, and returns
// the text that becomes the JSON object key.
//
// json renders a string-kind key from the string itself, and any other key
// type through MarshalText when it implements encoding.TextMarshaler; an
// integer key becomes its decimal form. The first two can carry text the
// ledger cannot keep: json.Marshal escapes a NUL in the key as \u0000 and
// replaces bytes that are not valid UTF-8 with U+FFFD, exactly as it does for
// a string value, so the key text needs the same check. A string-kind key is
// checked as its string, because json ignores a MarshalText on such a type.
func checkMapKey(k reflect.Value, path string) (string, error) {
	if k.Kind() == reflect.String {
		if !cairn.StorableText(k.String()) {
			return "", &NotStorableError{Path: path}
		}
		return k.String(), nil
	}
	if k.CanInterface() {
		if _, ok := k.Interface().(encoding.TextMarshaler); ok {
			// json.Marshal renders a nil pointer key as the empty string and
			// never calls its MarshalText.
			if k.Kind() == reflect.Pointer && k.IsNil() {
				return "", nil
			}
			return renderStorableText(k, path)
		}
	}
	// An integer key becomes its decimal form, which is always storable. The
	// value is only a path for anything else, which json.Marshal will reject.
	return k.String(), nil
}

// renderStorableText calls v's MarshalText and returns the text json.Marshal
// will write for it, refusing text the ledger cannot store.
//
// json writes a TextMarshaler's bytes through its own string encoder, so bytes
// that are not valid UTF-8 become U+FFFD — text the caller never sent — and a
// NUL is escaped as \u0000, which jsonb rejects. The bytes MarshalText returns
// are therefore not the bytes the ledger will hold. A value and a map key both
// reach that encoder, so both are checked here.
func renderStorableText(v reflect.Value, path string) (string, error) {
	text, err := v.Interface().(encoding.TextMarshaler).MarshalText()
	if err != nil {
		return "", err
	}
	if !cairn.StorableText(string(text)) {
		return "", &NotStorableError{Path: path}
	}
	return string(text), nil
}

// EncodeAll encodes a slice, preserving order.
func (c *Codec[T]) EncodeAll(vs []T) ([]Envelope, error) {
	out := make([]Envelope, 0, len(vs))
	for _, v := range vs {
		env, err := c.Encode(v)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

// Latest runs an envelope's data through its type's upcasters and returns the
// current version's JSON, without decoding it.
func (c *Codec[T]) Latest(env Envelope) (json.RawMessage, error) {
	_, data, err := c.upcast(env)
	return data, err
}

func (c *Codec[T]) upcast(env Envelope) (*entry, json.RawMessage, error) {
	e, ok := c.byName[env.Type]
	if !ok {
		return nil, nil, fmt.Errorf("codec: unknown type %q", env.Type)
	}
	if env.Version > e.version {
		return nil, nil, fmt.Errorf("codec: %s version %d is newer than this build's %d", env.Type, env.Version, e.version)
	}
	data := env.Data
	for v := env.Version; v < e.version; v++ {
		up, ok := e.upcasters[v]
		if !ok {
			return nil, nil, fmt.Errorf("codec: no upcaster for %s from version %d", env.Type, v)
		}
		var err error
		if data, err = up(data); err != nil {
			return nil, nil, fmt.Errorf("codec: upcasting %s from version %d: %w", env.Type, v, err)
		}
	}
	return e, data, nil
}

// Decode upcasts an envelope to its type's current version and unmarshals it.
func (c *Codec[T]) Decode(env Envelope) (T, error) {
	var zero T
	e, data, err := c.upcast(env)
	if err != nil {
		return zero, err
	}
	ptr := reflect.New(e.typ)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(ptr.Interface()); err != nil {
		return zero, fmt.Errorf("codec: decoding %s: %w", env.Type, err)
	}
	return ptr.Elem().Interface().(T), nil
}

// DecodeAll decodes a slice, preserving order.
func (c *Codec[T]) DecodeAll(envs []Envelope) ([]T, error) {
	out := make([]T, 0, len(envs))
	for _, env := range envs {
		v, err := c.Decode(env)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Registration identifies one type a Codec holds: the sum type's Go identity,
// the name it is stored under, its current version, and the versions that have
// an upcaster to the next one.
type Registration struct {
	Sum     string
	Name    string
	Version int
	Upcasts []int
}

// Registrations returns every type registered with c, sorted by sum type and
// name. It is the runtime half of the check against a generated manifest: a
// test compares what a program's codecs actually registered with what the
// static extractor wrote, so an upcaster that never runs, or a registration the
// extractor missed, fails there instead of when an old entry is decoded.
func (c *Codec[T]) Registrations() []Registration {
	sum := goTypeString(reflect.TypeFor[T]())
	out := make([]Registration, 0, len(c.byName))
	for _, e := range c.byName {
		ups := make([]int, 0, len(e.upcasters))
		for v := range e.upcasters {
			ups = append(ups, v)
		}
		slices.Sort(ups)
		out = append(out, Registration{Sum: sum, Name: e.name, Version: e.version, Upcasts: ups})
	}
	sortRegistrations(out)
	return out
}

// CheckRegistrations reports every difference between the codecs a manifest
// lists and the codecs a program built. want is the manifest's list, got the
// built codecs'. It returns one message per difference, sorted; an empty
// result means they agree.
func CheckRegistrations(want, got []Registration) []string {
	w, g := registrationMap(want), registrationMap(got)
	var problems []string
	for k, r := range w {
		o, ok := g[k]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s (%s) is in the manifest but not registered by the codecs", r.Name, r.Sum))
			continue
		case r.Version != o.Version:
			problems = append(problems, fmt.Sprintf("%s (%s) is version %d in the manifest but version %d in the codecs", r.Name, r.Sum, r.Version, o.Version))
			continue
		}
		for _, v := range r.Upcasts {
			if !slices.Contains(o.Upcasts, v) {
				problems = append(problems, fmt.Sprintf("%s (%s) has an upcaster from version %d in the manifest but not in the codecs", r.Name, r.Sum, v))
			}
		}
		for _, v := range o.Upcasts {
			if !slices.Contains(r.Upcasts, v) {
				problems = append(problems, fmt.Sprintf("%s (%s) has an upcaster from version %d in the codecs but not in the manifest", r.Name, r.Sum, v))
			}
		}
	}
	for k, r := range g {
		if _, ok := w[k]; !ok {
			problems = append(problems, fmt.Sprintf("%s (%s) is registered by the codecs but not in the manifest", r.Name, r.Sum))
		}
	}
	slices.Sort(problems)
	return problems
}

func registrationMap(rs []Registration) map[string]Registration {
	m := make(map[string]Registration, len(rs))
	for _, r := range rs {
		m[r.Sum+"\x00"+r.Name] = r
	}
	return m
}

func sortRegistrations(rs []Registration) {
	slices.SortFunc(rs, func(a, b Registration) int {
		return cmp.Or(cmp.Compare(a.Sum, b.Sum), cmp.Compare(a.Name, b.Name))
	})
}

// goTypeString renders a runtime type as go/types does for the manifest: a
// named type with its full import path, a basic type by name.
func goTypeString(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + goTypeString(t.Elem())
	case reflect.Slice:
		return "[]" + goTypeString(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), goTypeString(t.Elem()))
	case reflect.Map:
		return "map[" + goTypeString(t.Key()) + "]" + goTypeString(t.Elem())
	}
	if t.Name() == "" {
		return t.String()
	}
	if t.PkgPath() == "" {
		return t.Name()
	}
	return t.PkgPath() + "." + t.Name()
}

// Registrations returns the registrations of all four codecs, sorted.
func (s *Set[M, R, Ev, Ef]) Registrations() []Registration {
	codecs := []interface{ Registrations() []Registration }{s.Messages, s.Results, s.Events, s.Effects}
	var out []Registration
	for _, c := range codecs {
		out = append(out, c.Registrations()...)
	}
	sortRegistrations(out)
	return out
}

// Set is the four codecs a Decider's values are stored through: its messages,
// results, events and effects. The ledger stores every entry through one, and
// the checker round-trips every value through one (D6).
type Set[M, R, Ev, Ef any] struct {
	Messages *Codec[M]
	// Results encodes what an accepted message returns. A single struct R
	// registers as Register[R, R].
	Results *Codec[R]
	Events  *Codec[Ev]
	Effects *Codec[Ef]
}
