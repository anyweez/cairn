// Package namedrecurse names a pointer to the type itself.
package namedrecurse

import "github.com/anyweez/cairn/codec"

type T struct {
	V    int `json:"v"`
	Next *T  `json:"next"`
}

func (T) isEvent() {}

func Codecs() *codec.Codec[any] {
	c := codec.New[any]()
	codec.Register[T](c, "t", 1)
	return c
}
