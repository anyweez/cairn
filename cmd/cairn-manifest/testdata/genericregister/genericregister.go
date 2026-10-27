// A registration whose types come from a generic helper. The type argument
// written at the call is the helper's type parameter, whose JSON shape is its
// constraint rather than the shape of the type the caller instantiates it with,
// so the tool must refuse it rather than record the parameter.
package genericregister

import "github.com/anyweez/cairn/codec"

type Msg interface{ isMsg() }

type Event struct {
	Old int `json:"old"`
}

func (Event) isMsg() {}

func register[V Msg](c *codec.Codec[Msg]) {
	codec.Register[V, Msg](c, "generic.event", 1)
}

func registerSum[T any](c *codec.Codec[T]) {
	codec.Register[Event, T](c, "generic.sum", 1)
}

func upcastSum[T any](c *codec.Codec[T]) {
	c.Upcast("generic.upcast", 1, nil)
}

func Codecs() *codec.Codec[Msg] {
	c := codec.New[Msg]()
	register[Event](c)
	return c
}
