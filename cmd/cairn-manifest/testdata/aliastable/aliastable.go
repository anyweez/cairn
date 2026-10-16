// Package aliastable declares an fsm table and a rule through aliases of the
// cairn types. Both are package-level declarations the manifest lists.
package aliastable

import (
	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/spec"
)

type State string

const (
	Draft     State = "draft"
	Published State = "published"
)

// LifecycleTable is an alias of fsm.Table, which is legal Go.
type LifecycleTable = fsm.Table[State, string]

// Lifecycle is a package-level table declared with the alias.
var Lifecycle LifecycleTable = fsm.New(
	fsm.Edge[State, string]{From: Draft, On: "publish", To: Published},
	fsm.Edge[State, string]{From: Published, On: "retract", To: Draft},
)

// Rule is an alias of spec.Rule.
type Rule = spec.Rule

// AlwaysRetracts is a package-level rule declared with the alias.
var AlwaysRetracts Rule = Rule{ID: "A1", Kind: spec.Never, Status: spec.Established,
	Statement: "A published decision is retracted."}
