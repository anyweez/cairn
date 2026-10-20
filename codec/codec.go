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
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

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
// It walks the same values json.Marshal does. A type with its own MarshalText
// is checked through the text it returns, because json writes it through its
// own string encoder rather than verbatim. A type with its own MarshalJSON
// decides its own encoding, so the Go value behind it is not inspected; the
// bytes it returns are, because json.Marshal copies them into the entry and
// jsonb's alphabet is narrower than JSON's. A MarshalText or MarshalJSON on
// the pointer to the value counts too wherever json reaches the value
// addressably, which is a slice or array element, or a field of a struct
// behind a pointer.
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

// jsonNil reports whether json.Marshal renders v as null and so never calls
// MarshalJSON or MarshalText on it: a nil pointer, or a nil interface, or an
// interface whose dynamic value is a nil pointer. The dynamic value is what
// has to be tested — a Value of kind Interface is never of kind Pointer, even
// when it holds one.
func jsonNil(v reflect.Value) bool {
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return true
		}
		v = v.Elem()
	}
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func checkStorable(v reflect.Value, path string, seen map[visit]bool) error {
	if !v.IsValid() {
		return nil
	}
	// json.Marshal uses a pointer-receiver MarshalJSON or MarshalText whenever
	// the value is addressable — a slice or array element, or a struct behind a
	// pointer — and prefers it to a value-receiver method, so it is checked
	// first. A json.Marshaler decides its own bytes, but jsonb decides whether
	// it can keep them; a TextMarshaler's bytes go through json's string
	// encoder, so they are checked like any other string.
	if v.CanAddr() && v.Kind() != reflect.Pointer && v.Addr().CanInterface() {
		addr := v.Addr()
		if _, ok := addr.Interface().(json.Marshaler); ok {
			return checkStorableMarshaler(addr, path)
		}
		if _, ok := addr.Interface().(encoding.TextMarshaler); ok {
			_, err := renderStorableText(addr, path)
			return err
		}
	}
	if v.CanInterface() {
		if _, ok := v.Interface().(json.Marshaler); ok {
			return checkStorableMarshaler(v, path)
		}
		if _, ok := v.Interface().(encoding.TextMarshaler); ok {
			// json.Marshal renders a nil pointer as null and never calls
			// MarshalText — including a nil pointer held behind an interface.
			if jsonNil(v) {
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
		for _, f := range storableFields(v.Type()) {
			fv, err := v.FieldByIndexErr(f.index)
			if err != nil {
				// An embedded pointer on the path is nil, so json writes no
				// field beyond it.
				continue
			}
			if err := checkStorable(fv, path+"."+f.path, seen); err != nil {
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

// storableField is a field encoding/json writes for a struct type: the index
// path to it and the Go field names along that path, for an error message.
type storableField struct {
	index []int
	path  string
}

// storableFieldsCache holds the resolved fields of a struct type. The set
// depends only on the type, and Encode walks it for every entry.
var storableFieldsCache sync.Map // reflect.Type -> []storableField

// storableFields returns the fields encoding/json actually writes for a struct
// type, after resolving field-name dominance the way encoding/json does: a
// name reachable through more than one field keeps only its shallowest field,
// a tagged field beats an untagged one at the same depth, and any remaining
// tie drops the name. A field json drops is never stored, so its text is not a
// reason to refuse the value.
func storableFields(t reflect.Type) []storableField {
	if cached, ok := storableFieldsCache.Load(t); ok {
		return cached.([]storableField)
	}
	fields := computeStorableFields(t)
	storableFieldsCache.Store(t, fields)
	return fields
}

// fieldCandidate is one field encoding/json may write, before name dominance
// removes the ones it drops.
type fieldCandidate struct {
	name   string // the JSON key
	index  []int  // path from the root struct
	tagged bool   // its JSON tag named it, which breaks a depth tie
	path   string // its Go field names, for an error message
}

func computeStorableFields(t reflect.Type) []storableField {
	type queued struct {
		typ   reflect.Type
		index []int
		path  string
	}

	current := []queued{}
	next := []queued{{typ: t}}
	// count is how many times a type was queued at the current depth. A type
	// reachable twice contributes its fields twice, which is what makes the
	// dominance pass drop an ambiguous name rather than pick one.
	var count, nextCount map[reflect.Type]int
	visited := map[reflect.Type]bool{}
	var cands []fieldCandidate

	for len(next) > 0 {
		current, next = next, current[:0]
		count, nextCount = nextCount, map[reflect.Type]int{}
		for _, q := range current {
			if visited[q.typ] {
				continue
			}
			visited[q.typ] = true
			for i := 0; i < q.typ.NumField(); i++ {
				f := q.typ.Field(i)
				if f.Anonymous {
					ft := f.Type
					if ft.Kind() == reflect.Pointer {
						ft = ft.Elem()
					}
					// json.Marshal skips an unexported embedded non-struct, but
					// keeps an unexported embedded struct for its exported fields.
					if f.PkgPath != "" && ft.Kind() != reflect.Struct {
						continue
					}
				} else if f.PkgPath != "" {
					continue
				}
				tag := f.Tag.Get("json")
				if tag == "-" {
					continue
				}
				name, _, _ := strings.Cut(tag, ",")
				if !validJSONTag(name) {
					// json discards an invalid tag name and falls back to the Go
					// field name, so the field is untagged for dominance too.
					name = ""
				}
				tagged := name != ""

				index := make([]int, len(q.index)+1)
				copy(index, q.index)
				index[len(q.index)] = i
				fieldPath := f.Name
				if q.path != "" {
					fieldPath = q.path + "." + f.Name
				}

				ft := f.Type
				if ft.Name() == "" && ft.Kind() == reflect.Pointer {
					// json follows an unnamed pointer to an embedded struct.
					ft = ft.Elem()
				}
				if name != "" || !f.Anonymous || ft.Kind() != reflect.Struct {
					if name == "" {
						name = f.Name
					}
					c := fieldCandidate{name: name, index: index, tagged: tagged, path: fieldPath}
					cands = append(cands, c)
					if count[q.typ] > 1 {
						cands = append(cands, c)
					}
					continue
				}

				nextCount[ft]++
				if nextCount[ft] == 1 {
					next = append(next, queued{typ: ft, index: index, path: fieldPath})
				}
			}
		}
	}

	slices.SortFunc(cands, func(a, b fieldCandidate) int {
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
		// The first two are the shallowest. Still equal in depth and tag
		// presence, the name is ambiguous and json writes none of them.
		if len(cands[i].index) == len(cands[i+1].index) && cands[i].tagged == cands[i+1].tagged {
			continue
		}
		out = append(out, cands[i])
	}
	cands = out

	slices.SortFunc(cands, func(a, b fieldCandidate) int { return slices.Compare(a.index, b.index) })
	fields := make([]storableField, len(cands))
	for i, c := range cands {
		fields[i] = storableField{index: c.index, path: c.path}
	}
	return fields
}

// validJSONTag reports whether s may name a JSON field, mirroring
// encoding/json's isValidTag: letters, digits and a fixed set of punctuation.
// A name containing a quote, a backslash or a control character is rejected.
func validJSONTag(s string) bool {
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
			// never calls its MarshalText — including a nil pointer held
			// behind an interface.
			if jsonNil(k) {
				return "", nil
			}
			return renderStorableText(k, path)
		}
	}
	// An integer key becomes its decimal form, which is always storable. The
	// value is only a path for anything else, which json.Marshal will reject.
	return k.String(), nil
}

// checkStorableMarshaler calls v's MarshalJSON and checks the bytes jsonb will
// be handed. A nil pointer is storable because json writes null and never
// calls the method, and a marshaller that fails is left to Encode's own
// json.Marshal, which reports that failure once.
func checkStorableMarshaler(v reflect.Value, path string) error {
	if jsonNil(v) {
		return nil
	}
	data, err := v.Interface().(json.Marshaler).MarshalJSON()
	if err != nil {
		return nil
	}
	return checkMarshalledJSON(data, path)
}

// checkMarshalledJSON reports whether the JSON a MarshalJSON returned can be
// stored in jsonb and read back unchanged. json.Marshal compacts those bytes
// but does not sanitize them, so they reach the entry as they are: a string in
// them that is not valid UTF-8 is rejected by jsonb as an invalid byte
// sequence (SQLSTATE 22021), and a NUL or an unpaired UTF-16 surrogate escape
// is rejected as an unsupported Unicode escape (SQLSTATE 22P05). The bytes are
// the type's to decide, but what jsonb can keep is not.
func checkMarshalledJSON(data []byte, path string) error {
	if !utf8.Valid(data) {
		return &NotStorableError{Path: path}
	}
	for i := 0; i < len(data); {
		if data[i] != '\\' || i+1 >= len(data) {
			i++
			continue
		}
		if data[i+1] != 'u' {
			// An escaped quote, backslash or control character.
			i += 2
			continue
		}
		u, ok := jsonHex4(data[i+2:])
		if !ok {
			// Not JSON at all: json.Marshal refuses it, so there is no entry
			// to keep.
			return nil
		}
		switch {
		case u == 0, u >= 0xdc00 && u <= 0xdfff:
			// A NUL, or a low surrogate with no high surrogate before it.
			return &NotStorableError{Path: path}
		case u >= 0xd800 && u <= 0xdbff:
			// A high surrogate is storable only as half of a pair.
			lo, paired := rune(0), false
			if i+12 <= len(data) && data[i+6] == '\\' && data[i+7] == 'u' {
				lo, paired = jsonHex4(data[i+8:])
			}
			if !paired || lo < 0xdc00 || lo > 0xdfff {
				return &NotStorableError{Path: path}
			}
			i += 12
			continue
		}
		i += 6
	}
	return nil
}

// jsonHex4 reads the four hex digits of a \u escape, reporting whether b holds
// them.
func jsonHex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var u rune
	for _, c := range b[:4] {
		switch {
		case c >= '0' && c <= '9':
			c -= '0'
		case c >= 'a' && c <= 'f':
			c = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			c = c - 'A' + 10
		default:
			return 0, false
		}
		u = u<<4 | rune(c)
	}
	return u, true
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
	// A defined type is named however its kind looks: `type Listing []string`
	// is example.com/x.Listing to go/types, not []string. Ask for the name
	// before expanding a composite, or the runtime half of the manifest
	// cross-check names a different type than the static half.
	if t.Name() != "" {
		if t.PkgPath() == "" {
			return t.Name()
		}
		return t.PkgPath() + "." + t.Name()
	}
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
	return t.String()
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
