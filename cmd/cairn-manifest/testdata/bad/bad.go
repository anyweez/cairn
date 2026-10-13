// Package bad declares everything the manifest refuses to guess at.
package bad

import (
	"fmt"

	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

var statement = "not a constant"

var Dynamic = spec.Rule{ID: "B1", Statement: statement}

var First = spec.Rule{ID: "B2", Statement: "one"}

var Second = spec.Rule{ID: "B2", Statement: "two"}

var Copied = First

var edges = []fsm.Edge[string, string]{{From: "a", On: "go", To: "b"}}

var Spread = fsm.New(edges...)

type Msg interface{ isMsg() }

type Ping struct{}

func (Ping) isMsg() {}

func Codecs(version int) {
	msgs := codec.New[Msg]()
	codec.Register[Ping](msgs, fmt.Sprint("ping"), version)
	msgs.Upcast("missing", 1, nil)
}

var Positional = spec.Rule{"B3", spec.Never, spec.Established, "fields by position"}

func table() fsm.Table[string, string] { return fsm.New[string, string]() }

var Indirect = table()

var target = "b"

var NonConstEdge = fsm.New(fsm.Edge[string, string]{From: "a", On: "go", To: target})

func More(name string) {
	msgs := codec.New[Msg]()
	codec.Register[Ping](msgs, "pong", 1)
	codec.Register[Ping](msgs, "pong", 1)
	msgs.Upcast(name, 1, nil)
}
