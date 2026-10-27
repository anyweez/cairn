// Package methodupcast registers an upcaster through a method expression, so
// the codec is the call's first argument rather than its receiver. The method's
// declared receiver still names the codec the upcaster belongs to.
package methodupcast

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type V1 struct {
	X int `json:"x"`
}

func (V1) isEvent() {}

func Build() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[V1](c, "methodupcast.event", 2)
	(*codec.Codec[Event]).Upcast(c, "methodupcast.event", 1, nil)
	return c
}
