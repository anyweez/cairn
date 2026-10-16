// Package aliasrecurse names a pointer to the type itself through an alias.
package aliasrecurse

import "github.com/anyweez/cairn/codec"

type A = Node

type Node struct {
	V    int `json:"v"`
	Next *A  `json:"next"`
}

func Codecs() *codec.Codec[any] {
	c := codec.New[any]()
	codec.Register[Node](c, "node", 1)
	return c
}
