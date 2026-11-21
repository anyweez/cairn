// Package aliassumcomposite registers a stored type under a generic sum whose
// type arguments are composites holding an alias. reflect resolves the alias
// inside the composite too, so the runtime name of
// Event[[]AliasKey, *AliasKey] is Event[[]Key, *Key].
package aliassumcomposite

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

func Codecs() *codec.Codec[Event[[]AliasKey, *AliasKey]] {
	c := codec.New[Event[[]AliasKey, *AliasKey]]()
	codec.Register[Noted, Event[[]AliasKey, *AliasKey]](c, "aliassumcomposite.noted", 1)
	return c
}
