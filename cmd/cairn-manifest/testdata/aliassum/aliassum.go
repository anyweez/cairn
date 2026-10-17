// Package aliassum registers a stored type under a sum interface that is named
// through an alias. That is legal Go, and reflect reports the aliased type, not
// the alias: reflect.TypeFor[Msg]() is example.com/aliassum.Command.
package aliassum

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
	codec.Register[Ping](c, "aliassum.ping", 1)
	return c
}
