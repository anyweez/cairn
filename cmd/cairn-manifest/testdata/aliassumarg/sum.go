// Package aliassumarg registers a stored type under a generic sum whose type
// argument is written through an alias. That is legal Go, and reflect reports
// the aliased type, not the alias: the runtime identity of
// Event[AliasKey, string] is Event[Key, string].
package aliassumarg

import "github.com/anyweez/cairn/codec"

type Key struct {
	K string `json:"k"`
}

// AliasKey is an alias for Key, not a type of its own.
type AliasKey = Key

type Event[A any, B any] interface{ isEvent() }

type Noted struct {
	N int `json:"n"`
}

func (Noted) isEvent() {}

func Codecs() *codec.Codec[Event[AliasKey, string]] {
	c := codec.New[Event[AliasKey, string]]()
	codec.Register[Noted, Event[AliasKey, string]](c, "aliassumarg.noted", 1)
	return c
}
