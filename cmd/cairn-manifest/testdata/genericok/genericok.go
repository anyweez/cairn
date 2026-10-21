// Generic code that does not choose what is stored. A module using generics for
// its own helpers must still load, and a concrete registration records its shape
// as before.
package genericok

import "github.com/anyweez/cairn/codec"

type Msg interface{ isMsg() }

type Event struct {
	V int `json:"v"`
}

func (Event) isMsg() {}

func first[V any](vs ...V) V { return vs[0] }

func Codecs() *codec.Codec[Msg] {
	c := codec.New[Msg]()
	codec.Register[Event](c, "generic.ok", 1)
	_ = first(1)
	return c
}
