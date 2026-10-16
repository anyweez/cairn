// Package cairn is the pure core of a ledger-backed state machine.
//
// A stream's behaviour is a Decider: Decide looks at the current state and one
// message and says what happens, as events (facts that change state) and effects
// (side effects to carry out). Evolve folds one event into the state. Both are
// pure: no clock, no randomness, no I/O. Anything nondeterministic — the time,
// a minted ID, a roll of the dice, a fact read from another stream — is resolved
// into the message before Decide sees it, so the ledger entry that records the
// message is enough to replay the decision exactly.
//
// This package has no dependencies beyond the standard library. Code that
// implements a Decider should import nothing else that does I/O; the ledger and
// spectest packages are where the shell and the checks live.
package cairn

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Decider is a stream's behaviour.
//
//   - S is the state, folded from events.
//   - M is the message vocabulary: what can be asked of the stream.
//   - R is what a decision returns to the caller (an assigned ID, a count moved).
//   - Ev is the event vocabulary: the facts that are persisted and folded.
//   - Ef is the effect vocabulary: side effects carried out after commit.
//
// Evolve must be total over every event Decide has ever emitted, including
// events from older releases, because rebuilding state folds the stored events
// rather than re-deciding the stored messages.
type Decider[S, M, R, Ev, Ef any] struct {
	Initial func() S
	Decide  func(state S, msg M) Decision[R, Ev, Ef]
	Evolve  func(state S, event Ev) S
}

// Decision is what Decide returns for one message.
type Decision[R, Ev, Ef any] struct {
	Result R
	// Refusal is non-nil when the message was refused. A refused decision
	// carries no events and no effects; the ledger records the refusal itself.
	Refusal *Refusal
	Events  []Ev
	Effects []Ef
}

// Refused reports whether the decision refused its message.
func (d Decision[R, Ev, Ef]) Refused() bool { return d.Refusal != nil }

// Accept builds an accepting decision.
func Accept[R, Ev, Ef any](result R, events []Ev, effects ...Ef) Decision[R, Ev, Ef] {
	return Decision[R, Ev, Ef]{Result: result, Events: events, Effects: effects}
}

// Refuse builds a refusing decision. The code is the stable, comparable part:
// replay and spec diffs compare codes, never messages.
func Refuse[R, Ev, Ef any](code, format string, args ...any) Decision[R, Ev, Ef] {
	return Decision[R, Ev, Ef]{Refusal: &Refusal{Code: code, Message: fmt.Sprintf(format, args...)}}
}

// Refusal says why a message was refused.
type Refusal struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// StorableText reports whether s can be stored in a ledger and read back
// unchanged. Postgres stores text as UTF-8 and jsonb cannot represent a NUL,
// so a string that is not valid UTF-8 or contains a NUL cannot be stored at
// all; json.Marshal would otherwise quietly replace the offending bytes with
// the replacement rune U+FFFD, and the ledger would hold text the caller never
// wrote. A decider checks this to refuse what the ledger could not keep.
func StorableText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

// Is matches another refusal with the same code, so errors.Is works against a
// sentinel built with only a code.
func (r *Refusal) Is(target error) bool {
	t, ok := target.(*Refusal)
	return ok && t.Code == r.Code
}

// Fold applies events to a state in order.
func (d Decider[S, M, R, Ev, Ef]) Fold(state S, events []Ev) S {
	for _, e := range events {
		state = d.Evolve(state, e)
	}
	return state
}

// Step decides one message and folds its events, returning the decision and
// the state after it. A refused decision leaves the state as it was.
func (d Decider[S, M, R, Ev, Ef]) Step(state S, msg M) (Decision[R, Ev, Ef], S) {
	decision := d.Decide(state, msg)
	return decision, d.Fold(state, decision.Events)
}
