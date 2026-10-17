// Package alias declares a spec.Rule with an aliased literal: legal Go the
// manifest tool must read like any other rule.
package alias

import "github.com/anyweez/cairn/spec"

// Rule is an alias for spec.Rule.
type Rule = spec.Rule

var A1 spec.Rule = Rule{ID: "A1", Statement: "a rule written with an aliased literal"}
