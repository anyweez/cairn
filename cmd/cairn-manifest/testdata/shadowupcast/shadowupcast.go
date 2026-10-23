// Package shadowupcast declares a wrapper whose own Upcast hides the embedded
// codec's method, so the call names no codec the extractor can read.
package shadowupcast

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

type V1 struct {
	X int `json:"x"`
}

func (V1) isEvent() {}

type Events struct {
	*codec.Codec[Event]
}

// Upcast hides the promoted codec method.
func (e *Events) Upcast(name string, from int, f codec.Upcaster) {}

func Build() *codec.Codec[Event] {
	c := codec.New[Event]()
	codec.Register[V1](c, "shadowupcast.event", 2)
	e := &Events{Codec: c}
	e.Upcast("shadowupcast.event", 1, nil)
	return c
}
