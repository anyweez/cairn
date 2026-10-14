// Package spec is the vocabulary for stating what a Decider must and must not
// do: rules, and the laws and invariants that check them.
//
// The rules are the human-owned part. Each one has a stable ID, a one-sentence
// statement, a kind and a status. Review attention goes to changes in this
// set: adding or tightening a rule makes the system safer, while removing,
// loosening or demoting one makes it riskier.
//
// This package has no test-framework dependency, so production code can
// declare rules next to the state it constrains (as fsm tables do). The
// runner lives in spectest.
package spec

import (
	"fmt"

	"github.com/anyweez/cairn"
)

// Kind says what sort of promise a rule makes. It is for the reader and the
// manifest: the checker treats every rule alike, except that a rule checked
// by a reference model must be Refines, and no other rule may be.
type Kind int

const (
	// Never: no reachable transition or state may do this.
	Never Kind = iota
	// Can: whenever this is promised, it can actually happen.
	Can
	// Refines: the implementation agrees with a reference model.
	Refines
)

func (k Kind) String() string {
	switch k {
	case Never:
		return "never"
	case Can:
		return "can"
	case Refines:
		return "refines"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Status says whether a rule's violations fail the build.
type Status int

const (
	// Established rules gate: a violation fails the check.
	Established Status = iota
	// Proposed rules report: violations are listed but do not fail. A rule
	// starts here when the code does not yet satisfy it, so it can be
	// written down before it is enforced.
	Proposed
)

func (s Status) String() string {
	switch s {
	case Established:
		return "established"
	case Proposed:
		return "proposed"
	}
	return fmt.Sprintf("Status(%d)", int(s))
}

// Rule is one human-owned promise.
type Rule struct {
	ID        string
	Kind      Kind
	Status    Status
	Statement string
}

func (r Rule) String() string {
	return fmt.Sprintf("%s (%s, %s): %s", r.ID, r.Kind, r.Status, r.Statement)
}

// Verdict is one check's answer for one transition or state.
type Verdict struct {
	applies bool
	failure string
}

var (
	// Pass: the rule applied and held.
	Pass = Verdict{applies: true}
	// NA: the rule did not apply here. Counted separately from Pass so a rule
	// that never applies shows up as vacuous rather than as healthy.
	NA = Verdict{}
)

// Fail reports a violation.
func Fail(format string, args ...any) Verdict {
	return Verdict{applies: true, failure: fmt.Sprintf(format, args...)}
}

// Check is shorthand for Pass when ok, else a failure with the given detail.
func Check(ok bool, format string, args ...any) Verdict {
	if ok {
		return Pass
	}
	return Fail(format, args...)
}

// Applies reports whether the rule applied.
func (v Verdict) Applies() bool { return v.applies }

// Failed reports whether the rule was violated, and how.
func (v Verdict) Failed() (string, bool) { return v.failure, v.failure != "" }

// Transition is one decided message: the state before, the message, the
// decision and the state after folding its events.
type Transition[S, M, R, Ev, Ef any] struct {
	Before   S
	Msg      M
	Decision cairn.Decision[R, Ev, Ef]
	After    S
}

// Law checks a rule against one transition, decision included.
type Law[S, M, R, Ev, Ef any] struct {
	Rule  Rule
	Check func(Transition[S, M, R, Ev, Ef]) Verdict
}

// Change checks a rule against one state change: the state before a message,
// the message, and the state after. It is a Law that needs nothing from the
// decision, and most rules about a lifecycle are one. Its type parameters are
// inferred from the check, which a Law's cannot be, so a helper such as
// fsm.Law needs none spelled out.
type Change[S, M any] struct {
	Rule  Rule
	Check func(before S, msg M, after S) Verdict
}

// Invariant checks a rule against one state. Every state reached is checked,
// including the initial one.
type Invariant[S any] struct {
	Rule  Rule
	Check func(S) Verdict
}
