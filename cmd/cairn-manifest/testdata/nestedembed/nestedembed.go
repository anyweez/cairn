// Package nestedembed uses a self-embedding type as an ordinary field.
package nestedembed

import "github.com/anyweez/cairn/codec"

type Node struct {
	*Node
	V int `json:"v"`
}

type Outer struct {
	N Node `json:"n"`
}

func Codecs() *codec.Codec[any] {
	c := codec.New[any]()
	codec.Register[Outer](c, "outer", 1)
	return c
}
