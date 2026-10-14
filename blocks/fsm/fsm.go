// Package fsm states a lifecycle as a table of allowed transitions.
//
// The table is plain Go data rather than code, so it does three jobs at once:
// Decide asks it whether a transition is allowed, the spec checks that no
// state change ever happens outside it, and review tooling renders it as a
// diagram. Adding an edge is a loosening and shows up as one.
package fsm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anyweez/cairn/spec"
)

// Edge allows On to move a stream from From to To.
type Edge[St, On comparable] struct {
	From St
	On   On
	To   St
}

type key[St, On comparable] struct {
	from St
	on   On
}

// Table is a set of edges. At most one edge leaves a state for a given trigger.
type Table[St, On comparable] struct {
	edges []Edge[St, On]
	next  map[key[St, On]]St
}

// New builds a table, panicking if two edges leave the same state on the same
// trigger: the table is declared once, and an ambiguous one is a bug.
func New[St, On comparable](edges ...Edge[St, On]) Table[St, On] {
	t := Table[St, On]{next: map[key[St, On]]St{}}
	for _, e := range edges {
		k := key[St, On]{e.From, e.On}
		if _, dup := t.next[k]; dup {
			panic(fmt.Sprintf("fsm: two edges leave %v on %v", e.From, e.On))
		}
		t.next[k] = e.To
		t.edges = append(t.edges, e)
	}
	return t
}

// Next returns where on takes a stream in state from, if anywhere.
func (t Table[St, On]) Next(from St, on On) (St, bool) {
	to, ok := t.next[key[St, On]{from, on}]
	return to, ok
}

// Allows reports whether on may move a stream from from to to.
func (t Table[St, On]) Allows(from St, on On, to St) bool {
	next, ok := t.Next(from, on)
	return ok && next == to
}

// Edges returns the edges in declaration order.
func (t Table[St, On]) Edges() []Edge[St, On] { return append([]Edge[St, On](nil), t.edges...) }

// Mermaid renders the table as a Mermaid state diagram, edges sorted so the
// output is stable and diffs cleanly.
func (t Table[St, On]) Mermaid() string {
	lines := make([]string, 0, len(t.edges))
	for _, e := range t.edges {
		lines = append(lines, fmt.Sprintf("    %v --> %v: %v", e.From, e.To, e.On))
	}
	sort.Strings(lines)
	return "stateDiagram-v2\n" + strings.Join(lines, "\n") + "\n"
}

// Law is the rule a table implies: a stream's state changes only along an
// edge, for the message that edge names. Every type parameter is inferred.
//
//	fsm.Law(rule, table, statusOf, triggerOf)
func Law[S, M any, St, On comparable](
	rule spec.Rule,
	table Table[St, On],
	state func(S) St,
	trigger func(M) On,
) spec.Change[S, M] {
	return spec.Change[S, M]{
		Rule: rule,
		Check: func(before S, msg M, after S) spec.Verdict {
			from, to := state(before), state(after)
			if from == to {
				return spec.NA
			}
			on := trigger(msg)
			return spec.Check(table.Allows(from, on, to), "%v moved %v → %v, which the table does not allow", on, from, to)
		},
	}
}
