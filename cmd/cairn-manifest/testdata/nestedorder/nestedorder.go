// Package nestedorder registers one event whose shape contains a nested
// struct, so a manifest of it records that struct's field order inline.
package nestedorder

import "github.com/anyweez/cairn/codec"

type Event interface{ isEvent() }

// Outcome is the nested struct. Its field order is Go's, not the wire format's:
// JSON object keys are unordered.
type Outcome struct {
	Chosen    string `json:"chosen"`
	Rationale string `json:"rationale"`
	DecidedBy string `json:"decided_by"`
}

type Recorded struct {
	Outcome Outcome `json:"outcome"`
}

func (Recorded) isEvent() {}

func Codecs() *codec.Codec[Event] {
	events := codec.New[Event]()
	codec.Register[Recorded](events, "nestedorder.recorded", 1)
	return events
}
