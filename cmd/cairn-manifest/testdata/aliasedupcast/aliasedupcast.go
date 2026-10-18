// Package aliasedupcast registers an upcaster on a codec whose type is named
// through an alias of a pointer to codec.Codec. go/types keeps that alias, so
// the extractor has to resolve it before it can see the codec's type argument,
// or the upcaster never reaches the manifest.
package aliasedupcast

import "github.com/anyweez/cairn/codec"

type Command interface{ isCommand() }

type Ping struct {
	N int `json:"n"`
}

func (Ping) isCommand() {}

// Codecs is an alias for the codec type, so a variable declared with it has an
// alias receiver rather than a *codec.Codec[Command].
type Codecs = *codec.Codec[Command]

func Build() Codecs {
	var c Codecs = codec.New[Command]()
	codec.Register[Ping](c, "aliasedupcast.ping", 2)
	c.Upcast("aliasedupcast.ping", 1, nil)
	return c
}
