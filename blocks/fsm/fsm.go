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
// output is stable and diffs cleanly. The zero state is Mermaid's initial or
// final pseudo-state, [*], and any state that is not a plain Mermaid id is
// given a generated id with its own text as the label, so state and trigger
// values cannot become diagram syntax and still read as themselves.
func (t Table[St, On]) Mermaid() string {
	var zero St
	type state struct {
		id    string
		label string
		alias bool
	}
	states := map[St]state{}
	var order []St
	used := map[string]bool{}

	// Reserve the names Mermaid can read as ids before allocating aliases, so
	// an alias never collides with a state that already uses that name.
	for _, e := range t.edges {
		for _, s := range []St{e.From, e.To} {
			if s == zero {
				continue
			}
			if _, ok := states[s]; ok {
				continue
			}
			if label := fmt.Sprint(s); isMermaidID(label) {
				states[s] = state{id: label, label: label}
				used[label] = true
				order = append(order, s)
			}
		}
	}

	next := 0
	for _, e := range t.edges {
		for _, s := range []St{e.From, e.To} {
			if s == zero {
				continue
			}
			if _, ok := states[s]; ok {
				continue
			}
			var id string
			for {
				id = fmt.Sprintf("s%d", next)
				next++
				if !used[id] {
					break
				}
			}
			used[id] = true
			states[s] = state{id: id, label: mermaidLabel(fmt.Sprint(s)), alias: true}
			order = append(order, s)
		}
	}

	id := func(s St) string {
		if s == zero {
			return "[*]"
		}
		return states[s].id
	}
	var declarations, lines []string
	for _, s := range order {
		st := states[s]
		if st.alias {
			// Mermaid shows the quoted text as the state's name, decoding only
			// its own #NN; entities and not Go's escapes, so the quotes are
			// written around the label rather than applied with %q: mermaidLabel
			// has already escaped the characters that could end the string.
			declarations = append(declarations, fmt.Sprintf("    state \"%s\" as %s", st.label, st.id))
		}
	}
	for _, e := range t.edges {
		lines = append(lines, fmt.Sprintf("    %s --> %s: %s", id(e.From), id(e.To), mermaidLabel(fmt.Sprint(e.On))))
	}
	sort.Strings(declarations)
	sort.Strings(lines)
	out := append([]string{"stateDiagram-v2"}, declarations...)
	out = append(out, lines...)
	return strings.Join(out, "\n") + "\n"
}

// isMermaidID reports whether s can be written as a bare Mermaid state id.
// Values that are not plain ids get an alias, and keep their text as a label.
func isMermaidID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !mermaidAlnum(r) && r != '_' {
			return false
		}
	}
	switch strings.ToLower(s) {
	// Every word the state-diagram lexer reads at the start of a line as a
	// directive rather than a state id. The list is a copy of the lexer's
	// keywords, so it has to be kept complete: a missing one renders a state
	// as diagram syntax and loses the whole diagram.
	case "state", "statediagram", "direction", "note", "class", "classdef", "cssclass",
		"style", "default", "hide", "scale", "click", "href", "acctitle", "accdescr",
		"title", "end":
		return false
	}
	return true
}

// mermaidLabel escapes text that Mermaid would otherwise read as diagram
// syntax, or that would split the line. Mermaid's entity syntax is "#NN;" (no
// leading ampersand): the renderer decodes it back to the original character,
// so labels stay readable while a trigger containing "-->" cannot introduce an
// edge; such a label is escaped whole so the injected state's name does not
// appear in the diagram either. The colon is escaped for the same reason: the
// lexer ends the label at the separator, so a trigger like "set_status:open"
// would otherwise cut the line short and make the whole diagram unparseable.
func mermaidLabel(s string) string {
	whole := strings.Contains(s, "-->")
	var b strings.Builder
	for _, r := range s {
		if whole || mermaidSyntax(r) {
			fmt.Fprintf(&b, "#%d;", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func mermaidSyntax(r rune) bool {
	switch r {
	case '\n', '\r', '\t', '"', '&', ';', '%', '\\', '<', '>', '#', ':':
		return true
	}
	return false
}

func mermaidAlnum(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
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
