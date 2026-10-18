// Package aliasupcast registers an upcaster on a sum interface named through
// an alias, so the upcaster's sum has to resolve to the same type the
// registration does or the extractor cannot pair them.
package aliasupcast

import "github.com/anyweez/cairn/codec"

type Command interface{ isCommand() }

// Msg is an alias for Command.
type Msg = Command

type Ping struct {
	N int `json:"n"`
}

func (Ping) isCommand() {}

func Codecs() *codec.Codec[Msg] {
	c := codec.New[Msg]()
	codec.Register[Ping](c, "aliasupcast.ping", 2)
	c.Upcast("aliasupcast.ping", 1, nil)
	return c
}
