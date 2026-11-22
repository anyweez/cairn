// Package ifacesum registers a stored type under an unnamed interface sum whose
// method names its parameters and its result. go/types keeps those names in the
// type expression; reflect, which is what the runtime half of the cross-check
// names the sum with, has only their types.
package ifacesum

import "github.com/anyweez/cairn/codec"

type Accepted struct {
	X int `json:"x"`
}

func (Accepted) Accept(x int, done func(n int) error) (err error) { return nil }

func Codecs() *codec.Codec[interface {
	Accept(x int, done func(n int) error) (err error)
}] {
	c := codec.New[interface {
		Accept(x int, done func(n int) error) (err error)
	}]()
	codec.Register[Accepted](c, "ifacesum.accepted", 1)
	return c
}
