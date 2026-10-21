// Package decisions re-expresses lore's decision register on cairn.
//
// The register records what a team decided and why: the question, the options
// considered, one position per member, and the outcome. Its credibility rests
// on three properties, which lore enforces with row locks, status guards and a
// CHECK constraint, and which here are rules a spec checks:
//
//   - A decided decision carries its outcome.
//   - Positions and options freeze once a decision leaves open.
//   - A recorded outcome is never rewritten in place; changing it means
//     reopening, which clears the outcome and records what it cleared.
//
// Everything nondeterministic arrives in the message: the actor, the time, the
// IDs of new options, and — for Supersede — what the replacing decision looked
// like when it was read under lock.
//
// A decision is identified by the stream it is recorded on: a Create names that
// stream, and the ledger refuses it anywhere else, so State.ID and the stream
// are one identifier rather than two that happen to agree.
package decisions

import (
	"slices"
	"strings"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/codec"
)

// Status is where a decision is in its lifecycle. The zero value is a decision
// that has not been created.
type Status string

const (
	Uncreated  Status = ""
	Open       Status = "open"
	Decided    Status = "decided"
	Deferred   Status = "deferred"
	Dropped    Status = "dropped"
	Superseded Status = "superseded"
)

// Settled reports whether the status carries an outcome.
func (s Status) Settled() bool { return s == Decided || s == Superseded }

type Option struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Position is where one member stands. An empty Option means they weighed in
// without endorsing any option.
type Position struct {
	Option string `json:"option,omitempty"`
	Body   string `json:"body"`
	// Committed is the member's answer to the outcome: committing to it, or
	// maintaining their objection. Nil until they answer.
	Committed *bool `json:"committed,omitempty"`
}

type Outcome struct {
	Chosen    string    `json:"chosen"`
	Rationale string    `json:"rationale"`
	DecidedBy string    `json:"decided_by"`
	DecidedAt time.Time `json:"decided_at"`
}

// State is one decision.
type State struct {
	ID           string              `json:"id"`
	Title        string              `json:"title"`
	Status       Status              `json:"status"`
	Options      []Option            `json:"options"`
	Positions    map[string]Position `json:"positions"`
	Outcome      *Outcome            `json:"outcome,omitempty"`
	SupersededBy string              `json:"superseded_by,omitempty"`
}

func (s State) option(id string) bool {
	return slices.ContainsFunc(s.Options, func(o Option) bool { return o.ID == id })
}

// ---- messages ----------------------------------------------------------------

type Msg interface{ trigger() string }

type Create struct {
	ID      string    `json:"id"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
	Title   string    `json:"title"`
	Options []Option  `json:"options"`
}

type Edit struct {
	By    string `json:"by"`
	Title string `json:"title"`
}

type AddOption struct {
	By     string `json:"by"`
	Option Option `json:"option"`
}

type SavePosition struct {
	By     string `json:"by"`
	Option string `json:"option,omitempty"`
	Body   string `json:"body"`
}

type RecordOutcome struct {
	By        string    `json:"by"`
	At        time.Time `json:"at"`
	Chosen    string    `json:"chosen"`
	Rationale string    `json:"rationale"`
}

type SetStatus struct {
	By string `json:"by"`
	To Status `json:"to"`
}

// Supersede marks this decision replaced by another. The shell locks both
// streams and copies what it read of the replacement into the message, so the
// decision is made — and replayed — from facts in the ledger. With is the
// replacement decision's ID, which is the stream it is recorded on, so the
// self-supersede guard and DR3 compare it against this decision's ID like for
// like.
type Supersede struct {
	By               string `json:"by"`
	With             string `json:"with"`
	WithStatus       Status `json:"with_status"`
	WithSupersededBy string `json:"with_superseded_by,omitempty"`
}

type Acknowledge struct {
	By        string `json:"by"`
	Committed bool   `json:"committed"`
}

func (Create) trigger() string        { return "create" }
func (Edit) trigger() string          { return "edit" }
func (AddOption) trigger() string     { return "add_option" }
func (SavePosition) trigger() string  { return "save_position" }
func (RecordOutcome) trigger() string { return "record_outcome" }
func (m SetStatus) trigger() string   { return "set_status:" + string(m.To) }
func (Supersede) trigger() string     { return "supersede" }
func (Acknowledge) trigger() string   { return "acknowledge" }

// Trigger names what a message asks of the lifecycle.
func Trigger(m Msg) string { return m.trigger() }

// ---- events and effects --------------------------------------------------------

type Event interface{ isEvent() }

type Created struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Options []Option  `json:"options"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

type Edited struct {
	Title string `json:"title"`
}

type OptionAdded struct {
	Option Option `json:"option"`
}

type PositionSaved struct {
	Member string `json:"member"`
	Option string `json:"option,omitempty"`
	Body   string `json:"body"`
}

type DecisionRecorded struct {
	Outcome Outcome `json:"outcome"`
}

type StatusChanged struct {
	From Status `json:"from"`
	To   Status `json:"to"`
}

// Reopened erases a settled outcome, and keeps what it erased.
type Reopened struct {
	From                Status   `json:"from"`
	ClearedOutcome      *Outcome `json:"cleared_outcome,omitempty"`
	ClearedSupersededBy string   `json:"cleared_superseded_by,omitempty"`
}

type SupersededBy struct {
	By string `json:"by"`
}

type Acknowledged struct {
	Member    string `json:"member"`
	Committed bool   `json:"committed"`
}

func (Created) isEvent()          {}
func (Edited) isEvent()           {}
func (OptionAdded) isEvent()      {}
func (PositionSaved) isEvent()    {}
func (DecisionRecorded) isEvent() {}
func (StatusChanged) isEvent()    {}
func (Reopened) isEvent()         {}
func (SupersededBy) isEvent()     {}
func (Acknowledged) isEvent()     {}

type Effect interface{ isEffect() }

// IndexReferences asks the reference index to re-read the decision's text.
// It is an effect rather than a write in the transaction because the index is
// another service; the outbox delivers it at least once and not in order, so a
// consumer must ignore a delivery older than the text it holds. The delivery
// carries the producing entry's seq for that; see ReferenceIndex.
type IndexReferences struct {
	Decision string `json:"decision"`
	Text     string `json:"text"`
}

func (IndexReferences) isEffect() {}

// Result is what a successful message returns.
type Result struct {
	Status Status `json:"status"`
}

// ---- the lifecycle -------------------------------------------------------------

// Lifecycle is every status change the register allows. A settled decision
// moves only by being superseded or explicitly reopened.
var Lifecycle = fsm.New(
	fsm.Edge[Status, string]{From: Uncreated, On: "create", To: Open},
	fsm.Edge[Status, string]{From: Open, On: "record_outcome", To: Decided},
	fsm.Edge[Status, string]{From: Open, On: "set_status:deferred", To: Deferred},
	fsm.Edge[Status, string]{From: Open, On: "set_status:dropped", To: Dropped},
	fsm.Edge[Status, string]{From: Deferred, On: "set_status:open", To: Open},
	fsm.Edge[Status, string]{From: Deferred, On: "set_status:dropped", To: Dropped},
	fsm.Edge[Status, string]{From: Dropped, On: "set_status:open", To: Open},
	fsm.Edge[Status, string]{From: Dropped, On: "set_status:deferred", To: Deferred},
	fsm.Edge[Status, string]{From: Decided, On: "supersede", To: Superseded},
	fsm.Edge[Status, string]{From: Decided, On: "set_status:open", To: Open},
	fsm.Edge[Status, string]{From: Superseded, On: "set_status:open", To: Open},
)

// ---- the decider ---------------------------------------------------------------

type decision = cairn.Decision[Result, Event, Effect]

// Refusal codes. They are compared across releases by replay, so they are
// part of the register's contract.
const (
	NotFound      = "not_found"
	Exists        = "exists"
	Invalid       = "invalid"
	Frozen        = "frozen"
	UnknownOption = "unknown_option"
	NotAllowed    = "not_allowed"
	Circular      = "circular"
	NotDecided    = "not_decided"
	NoPosition    = "no_position"
)

func refuse(code, format string, args ...any) decision {
	return cairn.Refuse[Result, Event, Effect](code, format, args...)
}

func accept(s State, events ...Event) decision {
	return cairn.Accept[Result, Event, Effect](Result{Status: s.Status}, events)
}

// indexText is the text the reference index holds for a decision: its title,
// and its rationale once an outcome is recorded. Every message that changes
// that text derives the effect's Text from the state it produces, so the text
// the index is told is the decision's current text. Delivery order is not the
// decider's to give: a retried effect can arrive after a newer one, so the
// consumer drops a delivery older than the text it holds (ReferenceIndex).
func indexText(s State) string {
	if s.Outcome == nil {
		return s.Title
	}
	return s.Title + "\n" + s.Outcome.Rationale
}

// Decider is the register.
var Decider = cairn.Decider[State, Msg, Result, Event, Effect]{
	Initial: func() State { return State{} },
	Decide:  decide,
	Evolve:  evolve,
}

// withStorableTimes returns m with the times it carries in a form the codec
// can store faithfully. time.Time.MarshalJSON writes a zone offset only to
// the minute, so a zone whose offset has seconds in it (every tzdata zone
// before about 1900) would be stored as a different instant. UTC keeps the
// instant and is always writable, so only such a time is converted; a time
// nothing can make storable, such as a year outside [0,9999], is left for
// CheckStorable to refuse with the field it names.
func withStorableTimes(m Msg) Msg {
	switch m := m.(type) {
	case Create:
		m.At = storableTime(m.At)
		return m
	case RecordOutcome:
		m.At = storableTime(m.At)
		return m
	}
	return m
}

// storableTime returns t as the codec will carry it: unchanged when its own
// zone offset is one JSON can write, and in UTC otherwise, which is the same
// instant in a zone JSON writes exactly.
func storableTime(t time.Time) time.Time {
	if codec.CheckStorable(t) == nil {
		return t
	}
	return t.UTC()
}

func decide(s State, m Msg) decision {
	m = withStorableTimes(m)
	// The ledger stores text as UTF-8 and jsonb, so text it cannot hold must be
	// refused before the message is recorded. Otherwise the codec would replace
	// the offending bytes with U+FFFD and the caller would be told it succeeded.
	// A time it cannot carry as the same instant is refused here for the same
	// reason: the codec reports it, and the caller gets a refusal rather than an
	// encoding error after the decision.
	if err := codec.CheckStorable(m); err != nil {
		return refuse(Invalid, "%v", err)
	}
	if _, ok := m.(Create); !ok && s.Status == Uncreated {
		return refuse(NotFound, "no such decision")
	}
	// The shell stamps the entry's actor and time; it does not fill in the
	// message the caller sent. A message that names no sender would be recorded
	// in the log under the request's actor while the state it evolves names
	// nobody, so it is refused here. Create and RecordOutcome check their own
	// time where they store it.
	if sender(m) == "" {
		return refuse(Invalid, "a message needs a sender")
	}

	switch m := m.(type) {
	case Create:
		if s.Status != Uncreated {
			return refuse(Exists, "decision %s already exists", s.ID)
		}
		title := strings.TrimSpace(m.Title)
		if title == "" || m.ID == "" {
			return refuse(Invalid, "a decision needs a question")
		}
		if m.At.IsZero() {
			return refuse(Invalid, "a decision needs a creation time")
		}
		options := []Option{}
		seen := map[string]bool{}
		for _, o := range m.Options {
			title := strings.TrimSpace(o.Title)
			if title == "" || o.ID == "" {
				return refuse(Invalid, "an option needs a title")
			}
			if seen[o.ID] {
				return refuse(Invalid, "option %s already exists", o.ID)
			}
			seen[o.ID] = true
			options = append(options, Option{ID: o.ID, Title: title})
		}
		created := Created{ID: m.ID, Title: title, Options: options, By: m.By, At: m.At}
		return decision{
			Result:  Result{Status: Open},
			Events:  []Event{created},
			Effects: []Effect{IndexReferences{Decision: m.ID, Text: indexText(evolve(s, created))}},
		}

	case Edit:
		title := strings.TrimSpace(m.Title)
		if title == "" {
			return refuse(Invalid, "a decision needs a question")
		}
		if title == s.Title {
			return accept(s)
		}
		edited := Edited{Title: title}
		return decision{
			Result:  Result{Status: s.Status},
			Events:  []Event{edited},
			Effects: []Effect{IndexReferences{Decision: s.ID, Text: indexText(evolve(s, edited))}},
		}

	case AddOption:
		if s.Status != Open {
			return refuse(Frozen, "this decision is %s, so its options can no longer change", s.Status)
		}
		title := strings.TrimSpace(m.Option.Title)
		if title == "" || m.Option.ID == "" {
			return refuse(Invalid, "an option needs a title")
		}
		if s.option(m.Option.ID) {
			return refuse(Invalid, "option %s already exists", m.Option.ID)
		}
		return accept(s, OptionAdded{Option: Option{ID: m.Option.ID, Title: title}})

	case SavePosition:
		body := strings.TrimSpace(m.Body)
		if body == "" {
			return refuse(Invalid, "a position needs a body")
		}
		if s.Status != Open {
			return refuse(Frozen, "this decision is %s, so positions can no longer be changed", s.Status)
		}
		if m.Option != "" && !s.option(m.Option) {
			return refuse(UnknownOption, "that option does not belong to this decision")
		}
		return accept(s, PositionSaved{Member: m.By, Option: m.Option, Body: body})

	case RecordOutcome:
		rationale := strings.TrimSpace(m.Rationale)
		if rationale == "" || m.Chosen == "" {
			return refuse(Invalid, "recording a decision requires a chosen option and a rationale")
		}
		if m.At.IsZero() {
			return refuse(Invalid, "recording a decision requires a time")
		}
		if !s.option(m.Chosen) {
			return refuse(UnknownOption, "that option does not belong to this decision")
		}
		if _, ok := Lifecycle.Next(s.Status, m.trigger()); !ok {
			return refuse(NotAllowed, "this decision is no longer open")
		}
		outcome := Outcome{Chosen: m.Chosen, Rationale: rationale, DecidedBy: m.By, DecidedAt: m.At}
		recorded := DecisionRecorded{Outcome: outcome}
		return decision{
			Result:  Result{Status: Decided},
			Events:  []Event{recorded},
			Effects: []Effect{IndexReferences{Decision: s.ID, Text: indexText(evolve(s, recorded))}},
		}

	case SetStatus:
		if m.To != Open && m.To != Deferred && m.To != Dropped {
			return refuse(Invalid, "status must be open, deferred or dropped")
		}
		if m.To == s.Status {
			return accept(s)
		}
		to, ok := Lifecycle.Next(s.Status, m.trigger())
		if !ok {
			return refuse(NotAllowed, "a %s decision cannot be moved to %s; reopen it first", s.Status, m.To)
		}
		if s.Status.Settled() {
			reopened := Reopened{From: s.Status, ClearedOutcome: s.Outcome, ClearedSupersededBy: s.SupersededBy}
			return decision{Result: Result{Status: to}, Events: []Event{reopened},
				Effects: []Effect{IndexReferences{Decision: s.ID, Text: indexText(evolve(s, reopened))}}}
		}
		return decision{Result: Result{Status: to}, Events: []Event{StatusChanged{From: s.Status, To: to}}}

	case Supersede:
		switch {
		case m.With == s.ID || m.With == "":
			return refuse(Invalid, "a decision cannot supersede itself")
		case m.WithStatus != Decided:
			return refuse(NotAllowed, "only a decided decision can supersede another one")
		case m.WithSupersededBy == s.ID:
			return refuse(Circular, "that decision was itself replaced by this one")
		}
		to, ok := Lifecycle.Next(s.Status, m.trigger())
		if !ok {
			return refuse(NotAllowed, "only a decided decision can be superseded")
		}
		return decision{Result: Result{Status: to}, Events: []Event{SupersededBy{By: m.With}}}

	case Acknowledge:
		if s.Status != Decided {
			return refuse(NotDecided, "this decision has not been recorded yet")
		}
		if _, ok := s.Positions[m.By]; !ok {
			return refuse(NoPosition, "you did not record a position on this decision")
		}
		return accept(s, Acknowledged{Member: m.By, Committed: m.Committed})
	}
	return refuse(Invalid, "unknown message %T", m)
}

func evolve(s State, e Event) State {
	switch e := e.(type) {
	case Created:
		s.ID, s.Title, s.Status = e.ID, e.Title, Open
		s.Options = slices.Clone(e.Options)
		s.Positions = map[string]Position{}
	case Edited:
		s.Title = e.Title
	case OptionAdded:
		s.Options = append(slices.Clone(s.Options), e.Option)
	case PositionSaved:
		s.Positions = cloneMap(s.Positions)
		s.Positions[e.Member] = Position{Option: e.Option, Body: e.Body}
	case DecisionRecorded:
		outcome := e.Outcome
		s.Outcome, s.Status = &outcome, Decided
	case StatusChanged:
		s.Status = e.To
	case Reopened:
		s.Status, s.Outcome, s.SupersededBy = Open, nil, ""
		positions := make(map[string]Position, len(s.Positions))
		for member, p := range s.Positions {
			p.Committed = nil
			positions[member] = p
		}
		s.Positions = positions
	case SupersededBy:
		s.Status, s.SupersededBy = Superseded, e.By
	case Acknowledged:
		s.Positions = cloneMap(s.Positions)
		p := s.Positions[e.Member]
		committed := e.Committed
		p.Committed = &committed
		s.Positions[e.Member] = p
	}
	return s
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---- codecs --------------------------------------------------------------------

// Codecs are the register's stored types.
func Codecs() *codec.Set[Msg, Result, Event, Effect] {
	msgs := codec.New[Msg]()
	codec.Register[Create](msgs, "decision.create", 1)
	codec.Register[Edit](msgs, "decision.edit", 1)
	codec.Register[AddOption](msgs, "decision.add_option", 1)
	codec.Register[SavePosition](msgs, "decision.save_position", 1)
	codec.Register[RecordOutcome](msgs, "decision.record_outcome", 1)
	codec.Register[SetStatus](msgs, "decision.set_status", 1)
	codec.Register[Supersede](msgs, "decision.supersede", 1)
	codec.Register[Acknowledge](msgs, "decision.acknowledge", 1)

	results := codec.New[Result]()
	codec.Register[Result](results, "decision.result", 1)

	events := codec.New[Event]()
	codec.Register[Created](events, "decision.created", 1)
	codec.Register[Edited](events, "decision.edited", 1)
	codec.Register[OptionAdded](events, "decision.option_added", 1)
	codec.Register[PositionSaved](events, "decision.position_saved", 1)
	codec.Register[DecisionRecorded](events, "decision.recorded", 1)
	codec.Register[StatusChanged](events, "decision.status_changed", 1)
	codec.Register[Reopened](events, "decision.reopened", 1)
	codec.Register[SupersededBy](events, "decision.superseded", 1)
	codec.Register[Acknowledged](events, "decision.acknowledged", 1)

	effects := codec.New[Effect]()
	codec.Register[IndexReferences](effects, "decision.index_references", 1)
	return &codec.Set[Msg, Result, Event, Effect]{Messages: msgs, Results: results, Events: events, Effects: effects}
}
