// Package recurse embeds a pointer to the type itself.
package recurse

import "github.com/anyweez/cairn/codec"

type Node struct {
	*Node
	V int `json:"v"`
}

func (Node) isEvent() {}

func Codecs() *codec.Codec[any] {
	c := codec.New[any]()
	codec.Register[Node](c, "node", 1)
	return c
}
