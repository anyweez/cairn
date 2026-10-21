// Package unloadable declares a table and a rule that the manifest tool must
// not report as absent: it can't load the module at all.
package unloadable

import (
	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/spec"
)

type Status string

const (
	Draft Status = "draft"
	Live  Status = "live"
)

var Lifecycle = fsm.New(
	fsm.Edge[Status, string]{From: Draft, On: "publish", To: Live},
)

var NeverLiveWithoutPublishing = spec.Rule{ID: "U1", Kind: spec.Never, Status: spec.Established,
	Statement: "a decision is live without being published."}
