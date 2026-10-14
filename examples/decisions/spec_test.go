package decisions

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// The register's rules. Lore enforces most of these in SQL; here each is a
// statement a reviewer can read and a check the generator tries to break.
var (
	FollowsLifecycle = spec.Rule{ID: "DR1", Kind: spec.Never, Status: spec.Established,
		Statement: "A decision's status changes other than along an edge of Lifecycle."}
	SettledCarriesOutcome = spec.Rule{ID: "DR2", Kind: spec.Never, Status: spec.Established,
		Statement: "A decided or superseded decision lacks a complete outcome, or any other decision has one."}
	SupersededNamesReplacement = spec.Rule{ID: "DR3", Kind: spec.Never, Status: spec.Established,
		Statement: "A superseded decision does not name another decision as its replacement, or any other decision does."}
	PositionsNameOptions = spec.Rule{ID: "DR4", Kind: spec.Never, Status: spec.Established,
		Statement: "A position or an outcome names an option the decision does not have."}
	FreezeOnSettle = spec.Rule{ID: "DR5", Kind: spec.Never, Status: spec.Established,
		Statement: "Options or positions change while the decision is not open."}
	OutcomeNotRewritten = spec.Rule{ID: "DR6", Kind: spec.Never, Status: spec.Established,
		Statement: "A recorded outcome changes in place rather than being cleared by a reopen."}
	ReopenClearsAnswers = spec.Rule{ID: "DR7", Kind: spec.Never, Status: spec.Established,
		Statement: "Reopening a settled decision leaves any member's answer to the old outcome in place."}
	OnlyDissentersAnswer = spec.Rule{ID: "DR8", Kind: spec.Never, Status: spec.Proposed,
		Statement: "A member whose position endorsed the chosen option records a disagree-and-commit answer."}
)

var members = []string{"ana", "ben", "cy"}

func messages(t *rapid.T, s State) Msg {
	// Mostly a real option, sometimes none or one that does not exist.
	optionIDs := func() string {
		ids := []string{"", "o-none"}
		for _, o := range s.Options {
			ids = append(ids, o.ID, o.ID, o.ID)
		}
		return rapid.SampledFrom(ids).Draw(t, "option")
	}
	member := func() string { return rapid.SampledFrom(members).Draw(t, "by") }
	// Mostly a member who has a position, sometimes anyone.
	positioned := func() string {
		candidates := slices.Clone(members)
		for m := range s.Positions {
			candidates = append(candidates, m, m, m)
		}
		slices.Sort(candidates)
		return rapid.SampledFrom(candidates).Draw(t, "by")
	}
	text := func(label string) string {
		return rapid.SampledFrom([]string{"", "  ", "ship it", "wait a quarter", "ship it "}).Draw(t, label)
	}
	at := func() time.Time {
		return time.Unix(rapid.Int64Range(1_700_000_000, 1_800_000_000).Draw(t, "at"), 0).UTC()
	}
	status := func(label string) Status {
		return rapid.SampledFrom([]Status{Open, Decided, Deferred, Dropped, Superseded, "bogus"}).Draw(t, label)
	}

	// Weighted toward what the current status makes interesting, so runs reach
	// a decided decision with positions on it, and every message still turns up
	// in every status to exercise the refusals.
	kinds := []string{"create", "edit", "add_option", "save_position", "record_outcome", "set_status", "supersede", "acknowledge"}
	switch s.Status {
	case Uncreated:
		kinds = []string{"create", "create", "create", "edit"}
	case Open:
		kinds = append(kinds, "add_option", "save_position", "save_position", "save_position", "record_outcome", "record_outcome")
	case Decided, Superseded:
		// set_status rarely draws a particular status, so the moves the rules
		// guard are drawn directly, often enough to be tried in every check:
		// reopening (DR7), moving sideways to deferred or dropped (DR1), and
		// recording again (DR6).
		kinds = append(kinds, "acknowledge", "acknowledge", "acknowledge", "set_status", "set_status", "reopen", "supersede", "save_position",
			"sideways", "sideways", "sideways", "record_outcome", "record_outcome", "record_outcome")
	}
	switch rapid.SampledFrom(kinds).Draw(t, "kind") {
	case "create":
		n := rapid.IntRange(0, 3).Draw(t, "options")
		options := make([]Option, n)
		for i := range options {
			options[i] = Option{ID: fmt.Sprintf("o%d", i+1), Title: text("option title")}
		}
		return Create{ID: "d-1", By: member(), At: at(), Title: text("title"), Options: options}
	case "edit":
		return Edit{By: member(), Title: text("title")}
	case "add_option":
		return AddOption{By: member(), Option: Option{ID: fmt.Sprintf("o%d", len(s.Options)+1), Title: text("option title")}}
	case "save_position":
		return SavePosition{By: member(), Option: optionIDs(), Body: text("body")}
	case "record_outcome":
		return RecordOutcome{By: member(), At: at(), Chosen: optionIDs(), Rationale: text("rationale")}
	case "set_status":
		return SetStatus{By: member(), To: status("to")}
	case "reopen":
		return SetStatus{By: member(), To: Open}
	case "sideways":
		return SetStatus{By: member(), To: rapid.SampledFrom([]Status{Deferred, Dropped}).Draw(t, "to")}
	case "supersede":
		return Supersede{
			By:               member(),
			With:             rapid.SampledFrom([]string{"d-2", "d-1", ""}).Draw(t, "with"),
			WithStatus:       status("with status"),
			WithSupersededBy: rapid.SampledFrom([]string{"", "d-1", "d-3"}).Draw(t, "with superseded by"),
		}
	default:
		return Acknowledge{By: positioned(), Committed: rapid.Bool().Draw(t, "committed")}
	}
}

type transition = spec.Transition[State, Msg, Result, Event, Effect]

func machine() spectest.Machine[State, Msg, Result, Event, Effect] {
	return spectest.Machine[State, Msg, Result, Event, Effect]{
		Decider:   Decider,
		Messages:  messages,
		Codecs:    Codecs(),
		Snapshots: true,
		Invariants: []spec.Invariant[State]{
			{Rule: SettledCarriesOutcome, Check: func(s State) spec.Verdict {
				complete := s.Outcome != nil && s.Outcome.Chosen != "" && s.Outcome.Rationale != "" &&
					s.Outcome.DecidedBy != "" && !s.Outcome.DecidedAt.IsZero()
				if s.Status.Settled() {
					return spec.Check(complete, "%s with outcome %+v", s.Status, s.Outcome)
				}
				return spec.Check(s.Outcome == nil, "%s yet carries outcome %+v", s.Status, s.Outcome)
			}},
			{Rule: SupersededNamesReplacement, Check: func(s State) spec.Verdict {
				if s.Status == Superseded {
					return spec.Check(s.SupersededBy != "" && s.SupersededBy != s.ID, "superseded by %q", s.SupersededBy)
				}
				return spec.Check(s.SupersededBy == "", "%s yet superseded by %q", s.Status, s.SupersededBy)
			}},
			{Rule: PositionsNameOptions, Check: func(s State) spec.Verdict {
				if s.Status == Uncreated {
					return spec.NA
				}
				for member, p := range s.Positions {
					if p.Option != "" && !s.option(p.Option) {
						return spec.Fail("%s's position names %s", member, p.Option)
					}
				}
				if s.Outcome != nil && !s.option(s.Outcome.Chosen) {
					return spec.Fail("outcome chose %s", s.Outcome.Chosen)
				}
				return spec.Pass
			}},
		},
		Changes: []spec.Change[State, Msg]{
			fsm.Law(FollowsLifecycle, Lifecycle, func(s State) Status { return s.Status }, Trigger),
			{Rule: FreezeOnSettle, Check: func(before State, m Msg, after State) spec.Verdict {
				if before.Status == Open || before.Status == Uncreated {
					return spec.NA
				}
				stances := func(s State) map[string]Position {
					out := map[string]Position{}
					for m, p := range s.Positions {
						out[m] = Position{Option: p.Option, Body: p.Body}
					}
					return out
				}
				return spec.Check(slices.Equal(before.Options, after.Options) &&
					maps.Equal(stances(before), stances(after)),
					"%T changed options or positions on a %s decision", m, before.Status)
			}},
			{Rule: OutcomeNotRewritten, Check: func(before State, _ Msg, after State) spec.Verdict {
				if before.Outcome == nil || after.Outcome == nil {
					return spec.NA
				}
				return spec.Check(*before.Outcome == *after.Outcome, "outcome went %+v → %+v", *before.Outcome, *after.Outcome)
			}},
			{Rule: ReopenClearsAnswers, Check: func(before State, _ Msg, after State) spec.Verdict {
				if !before.Status.Settled() || after.Status != Open {
					return spec.NA
				}
				for member, p := range after.Positions {
					if p.Committed != nil {
						return spec.Fail("%s's answer survived the reopen", member)
					}
				}
				return spec.Pass
			}},
		},
		Laws: []spec.Law[State, Msg, Result, Event, Effect]{
			{Rule: OnlyDissentersAnswer, Check: func(t transition) spec.Verdict {
				ack, ok := t.Msg.(Acknowledge)
				if !ok || t.Decision.Refused() {
					return spec.NA
				}
				p := t.Before.Positions[ack.By]
				return spec.Check(p.Option != t.Before.Outcome.Chosen,
					"%s endorsed the winning option %s, yet answered committed=%v", ack.By, p.Option, ack.Committed)
			}},
		},
	}
}

func TestRegisterSpec(t *testing.T) {
	// DR8 is proposed because the register does not satisfy it yet: lore lets
	// anyone with a position answer the outcome, though the answer is meant for
	// the losing side. It is reported, not failed; when the decider refuses
	// those answers, it stops being reported and should be promoted.
	for _, v := range machine().CheckReport(t) {
		if v.Rule.ID != OnlyDissentersAnswer.ID {
			t.Errorf("unexpected proposed-rule violation: %+v", v)
		}
	}
}

// The DR8 gap as a fixed example: a member who backed the winning option can
// still record a disagree-and-commit answer.
func TestWinnerMayStillAnswerTheOutcome(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	state := Decider.Initial()
	for _, m := range []Msg{
		Create{ID: "d-1", By: "ana", At: at, Title: "ship it?", Options: []Option{{ID: "o1", Title: "yes"}, {ID: "o2", Title: "no"}}},
		SavePosition{By: "ana", Option: "o1", Body: "yes"},
		RecordOutcome{By: "ben", At: at, Chosen: "o1", Rationale: "consensus"},
	} {
		var d decision
		if d, state = Decider.Step(state, m); d.Refused() {
			t.Fatalf("%T refused: %v", m, d.Refusal)
		}
	}
	if d := Decider.Decide(state, Acknowledge{By: "ana", Committed: true}); d.Refused() {
		t.Fatalf("the gap is closed (refused %v): promote DR8 to established and delete this test", d.Refusal)
	}
}

// A mutant decider must fail the spec: this is how the rules prove they have teeth.
func TestRegisterSpecCatchesMutants(t *testing.T) {
	type mutant struct {
		rule   string // the rule that must catch it
		decide func(State, Msg) decision
	}
	mutants := map[string]mutant{
		// Positions keep changing after the outcome is recorded.
		"no freeze": {"DR5", func(s State, m Msg) decision {
			if p, ok := m.(SavePosition); ok && s.Status == Decided && p.Body != "" && (p.Option == "" || s.option(p.Option)) {
				return accept(s, PositionSaved{Member: p.By, Option: p.Option, Body: p.Body})
			}
			return decide(s, m)
		}},
		// Dropping a decided decision without reopening it.
		"settled moves sideways": {"DR1", func(s State, m Msg) decision {
			if st, ok := m.(SetStatus); ok && s.Status == Decided && st.To == Dropped {
				return decision{Result: Result{Status: Dropped}, Events: []Event{StatusChanged{From: s.Status, To: Dropped}}}
			}
			return decide(s, m)
		}},
		// Recording again overwrites the outcome in place.
		"outcome rewritten": {"DR6", func(s State, m Msg) decision {
			if r, ok := m.(RecordOutcome); ok && s.Status == Decided && s.option(r.Chosen) && r.Rationale != "" {
				return accept(s, DecisionRecorded{Outcome: Outcome{Chosen: r.Chosen, Rationale: r.Rationale, DecidedBy: r.By, DecidedAt: r.At}})
			}
			return decide(s, m)
		}},
	}
	for name, mut := range mutants {
		t.Run(name, func(t *testing.T) {
			m := machine()
			m.Decider.Decide = mut.decide
			msg, failed := spectest.Fails(t, m.Check)
			if !strings.Contains(msg, mut.rule+" violated") {
				t.Fatalf("the %q mutant must be caught by %s; failed %v: %s", name, mut.rule, failed, firstLine(msg))
			}
			t.Logf("caught: %s", firstLine(msg))
		})
	}

	// An Evolve that writes into the map it shares with the previous state is
	// caught by the framework's own isolation rule.
	t.Run("evolve aliases positions", func(t *testing.T) {
		m := machine()
		m.Decider.Evolve = func(s State, e Event) State {
			if p, ok := e.(PositionSaved); ok && s.Positions != nil {
				s.Positions[p.Member] = Position{Option: p.Option, Body: p.Body}
				return s
			}
			return evolve(s, e)
		}
		msg, _ := spectest.Fails(t, m.Check)
		if !strings.Contains(msg, spectest.Isolated.ID+" violated") {
			t.Fatalf("aliasing Evolve must be caught by %s: %s", spectest.Isolated.ID, firstLine(msg))
		}
		t.Logf("caught: %s", firstLine(msg))
	})
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
