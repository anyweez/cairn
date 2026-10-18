// Package embedalias embeds a pointer to itself through an alias.
package embedalias

import "github.com/anyweez/cairn/codec"

type A = Node

type Node struct {
	*A
	V int `json:"v"`
}

func Codecs() *codec.Codec[any] {
	c := codec.New[any]()
	codec.Register[Node](c, "node", 1)
	return c
}
