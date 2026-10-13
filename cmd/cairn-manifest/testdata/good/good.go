// Package good declares one of everything the manifest reads.
package good

import (
	"encoding/json"
	"time"

	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

type State string

const (
	Draft     State = "draft"
	Published State = "published"
)

var Lifecycle = fsm.New(
	fsm.Edge[State, string]{From: Published, On: "retract", To: Draft},
	fsm.Edge[State, string]{From: Draft, On: "publish", To: Published},
)

var NoSkippedDrafts = spec.Rule{ID: "G1", Kind: spec.Can, Status: spec.Proposed,
	Statement: "A draft can always be published."}

// Defaults leaves Kind and Status at their zero values.
var Defaults = spec.Rule{ID: "G2", Statement: "Zero values are never and established."}

type Event interface{ isEvent() }

type Meta struct {
	By string `json:"by"`
}

type Node struct {
	Next *Node `json:"next,omitempty"`
}

type Published_ struct {
	Meta
	Title    string          `json:"title"`
	Body     []byte          `json:"body"`
	Tags     map[string]int  `json:"tags,omitempty"`
	At       time.Time       `json:"at"`
	Raw      json.RawMessage `json:"raw"`
	Chain    Node            `json:"chain"`
	Status   State           `json:"status"`
	Skipped  string          `json:"-"`
	Dash     string          `json:"-,"`
	Untagged int
	hidden   int
	Extra    map[string]string `json:"extra,omitzero"`
}

func (Published_) isEvent() {}

func Codecs() *codec.Codec[Event] {
	events := codec.New[Event]()
	codec.Register[Published_](events, "good.published", 3)
	events.Upcast("good.published", 1, nil)
	events.Upcast("good.published", 2, nil)
	return events
}

func local() {
	// Built inside a function: not part of the manifest.
	_ = spec.Rule{ID: "LOCAL", Statement: "ignored"}
	_ = fsm.New(fsm.Edge[State, string]{From: Draft, On: "x", To: Draft})
	_ = Published_{}.hidden
}
