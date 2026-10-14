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
	"encoding/json"
	"fmt"
	"reflect"
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
func (c *Codec[T]) Encode(v T) (Envelope, error) {
	typ := reflect.TypeOf(any(v))
	e, ok := c.byType[typ]
	if !ok {
		return Envelope{}, fmt.Errorf("codec: %v is not registered", typ)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return Envelope{}, fmt.Errorf("codec: encoding %s: %w", e.name, err)
	}
	return Envelope{Type: e.name, Version: e.version, Data: data}, nil
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
