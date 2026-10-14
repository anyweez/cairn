// Package spectest checks a Decider against its rules by generating message
// sequences with rapid and judging every transition.
//
// Besides the rules a machine declares, every machine is held to the
// framework's own rules (the D-series below). They are what make the ledger
// trustworthy: a decision that is not deterministic cannot be replayed, and an
// Evolve that mutates its input makes the state a stream rebuilds differ from
// the one it ran with.
package spectest

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
)

// The framework's rules. Every machine is held to them.
var (
	Deterministic = spec.Rule{ID: "D1", Kind: spec.Never, Status: spec.Established,
		Statement: "The same state and message produce two different decisions."}
	Total = spec.Rule{ID: "D2", Kind: spec.Never, Status: spec.Established,
		Statement: "Decide or Evolve panics."}
	RefusalInert = spec.Rule{ID: "D3", Kind: spec.Never, Status: spec.Established,
		Statement: "A refused decision carries events or effects."}
	Isolated = spec.Rule{ID: "D4", Kind: spec.Never, Status: spec.Established,
		Statement: "Decide or Evolve modifies the state it was given."}
	Replayable = spec.Rule{ID: "D5", Kind: spec.Never, Status: spec.Established,
		Statement: "Folding a run's events from the initial state ends anywhere but where the run did."}
	Encodable = spec.Rule{ID: "D6", Kind: spec.Never, Status: spec.Established,
		Statement: "A message, result, event or effect does not survive encoding and decoding unchanged."}
	Snapshottable = spec.Rule{ID: "D7", Kind: spec.Never, Status: spec.Established,
		Statement: "A state does not survive a JSON snapshot round trip unchanged."}
)

// Machine is a Decider with everything needed to check it.
type Machine[S, M, R, Ev, Ef any] struct {
	Decider cairn.Decider[S, M, R, Ev, Ef]

	// Messages draws the next message for the current state. Draw from the
	// state (an existing option, a member who has a position) to reach deep
	// states, and draw unconstrained values too, or refusals go untested.
	Messages func(t *rapid.T, state S) M

	Laws       []spec.Law[S, M, R, Ev, Ef]
	Changes    []spec.Change[S, M]
	Invariants []spec.Invariant[S]
	Models     []Refinement[S, M, R]

	// Equal compares states. Defaults to reflect.DeepEqual.
	Equal func(a, b S) bool

	// Steps caps the messages in one run. Defaults to 40.
	Steps int

	// Codecs, when given, are round-tripped on every message, accepted result,
	// event and effect. A nil codec in the set is not checked.
	Codecs *codec.Set[M, R, Ev, Ef]

	// Snapshots says the state is persisted as JSON, so it must round-trip.
	Snapshots bool

	// AllowVacuous lists the IDs of established rules that may pass without
	// ever applying. Any other established rule that never applies in a full
	// Check fails it: the generator never reaches the rule, so it proves
	// nothing.
	AllowVacuous []string
}

// vacuityRuns is how many runs a Check needs before an established rule that
// never applied fails it. It is rapid's default check count. Shorter checks
// (-short, a lowered -rapid.checks, a test deadline) only report vacuity,
// since a rarely applied rule can miss a few runs by chance.
const vacuityRuns = 100

// Check runs the machine under rapid. Established rules fail the test, with a
// shrunk counterexample, as does an established rule that never applies;
// proposed rules and rule health are reported in the log.
func (m Machine[S, M, R, Ev, Ef]) Check(t rapid.TB) {
	t.Helper()
	m.CheckReport(t)
}

func (m Machine[S, M, R, Ev, Ef]) rules() []spec.Rule {
	rules := []spec.Rule{Deterministic, Total, RefusalInert, Isolated, Replayable}
	if m.Codecs != nil {
		rules = append(rules, Encodable)
	}
	if m.Snapshots {
		rules = append(rules, Snapshottable)
	}
	for _, l := range m.Laws {
		rules = append(rules, l.Rule)
	}
	for _, c := range m.Changes {
		rules = append(rules, c.Rule)
	}
	for _, i := range m.Invariants {
		rules = append(rules, i.Rule)
	}
	for _, r := range m.Models {
		rules = append(rules, r.rule())
	}
	return rules
}

// judge records a verdict. An established failure stops the run so rapid can
// shrink it; a proposed one is noted and the run goes on.
func judge(rt *rapid.T, rep *report, trace *[]string, rule spec.Rule, v spec.Verdict) {
	rep.count(rule, v)
	detail, failed := v.Failed()
	if !failed {
		return
	}
	if rule.Status == spec.Proposed {
		rep.proposed(rule, detail, *trace)
		return
	}
	rt.Fatalf("%s violated: %s\n  rule: %s", rule.ID, detail, rule.Statement)
}

func (m Machine[S, M, R, Ev, Ef]) run(rt *rapid.T, rep *report) {
	rep.ran()
	var trace []string
	j := func(rule spec.Rule, v spec.Verdict) { judge(rt, rep, &trace, rule, v) }

	initial, ok := guard(func() S { return m.Decider.Initial() })
	j(Total, spec.Check(ok, "Initial panicked: %v", initial.panic))
	state := initial.value

	models := make([]refinementRun[S, M, R], len(m.Models))
	for i, r := range m.Models {
		models[i] = r.start()
		j(r.rule(), models[i].agrees(state))
	}
	m.checkState(j, state)

	var events []Ev
	steps := rapid.IntRange(1, m.Steps).Draw(rt, "steps")
	for step := 1; step <= steps; step++ {
		msg := m.Messages(rt, state)
		trace = append(trace, fmt.Sprintf("%d. %s", step, describe(msg)))

		before := fingerprint(state)
		first, ok := guard(func() cairn.Decision[R, Ev, Ef] { return m.Decider.Decide(state, msg) })
		j(Total, spec.Check(ok, "Decide panicked on %+v: %v", msg, first.panic))
		decision := first.value
		j(Isolated, spec.Check(fingerprint(state) == before, "Decide modified its state for %+v", msg))

		second, secondOK := guard(func() cairn.Decision[R, Ev, Ef] { return m.Decider.Decide(state, msg) })
		j(Total, spec.Check(secondOK, "Decide panicked on its second call with %+v: %v", msg, second.panic))
		if ok && secondOK {
			j(Deterministic, spec.Check(reflect.DeepEqual(decision, second.value),
				"decided %+v, then %+v", decision, second.value))
		}

		j(RefusalInert, spec.Check(!decision.Refused() || (len(decision.Events) == 0 && len(decision.Effects) == 0),
			"refused %s but carried %d events and %d effects", decision.Refusal, len(decision.Events), len(decision.Effects)))

		after := state
		for _, e := range decision.Events {
			mark := fingerprint(after)
			next, ok := guard(func() S { return m.Decider.Evolve(after, e) })
			j(Total, spec.Check(ok, "Evolve panicked on %+v: %v", e, next.panic))
			j(Isolated, spec.Check(fingerprint(after) == mark, "Evolve modified its state for %+v", e))
			after = next.value
		}

		if decision.Refused() {
			trace[len(trace)-1] += fmt.Sprintf(" → refused %s", decision.Refusal.Code)
		} else {
			trace[len(trace)-1] += " → " + describeAll(decision.Events)
		}
		rt.Log(trace[len(trace)-1])

		m.checkCodecs(j, msg, decision)

		tr := spec.Transition[S, M, R, Ev, Ef]{Before: state, Msg: msg, Decision: decision, After: after}
		for _, l := range m.Laws {
			j(l.Rule, checked(func() spec.Verdict { return l.Check(tr) }))
		}
		for _, c := range m.Changes {
			j(c.Rule, checked(func() spec.Verdict { return c.Check(state, msg, after) }))
		}
		for i, r := range models {
			for _, v := range r.step(msg, decision.Result, decision.Refusal, after) {
				j(m.Models[i].rule(), v)
			}
		}

		events = append(events, decision.Events...)
		state = after
		m.checkState(j, state)
	}

	replayed := m.Decider.Fold(m.Decider.Initial(), events)
	j(Replayable, spec.Check(m.Equal(replayed, state), "folded %+v, ran to %+v", replayed, state))
}

func (m Machine[S, M, R, Ev, Ef]) checkState(j func(spec.Rule, spec.Verdict), state S) {
	for _, inv := range m.Invariants {
		j(inv.Rule, checked(func() spec.Verdict { return inv.Check(state) }))
	}
	if m.Snapshots {
		data, err := json.Marshal(state)
		var back S
		if err == nil {
			err = json.Unmarshal(data, &back)
		}
		j(Snapshottable, spec.Check(err == nil && m.Equal(state, back), "state %+v came back as %+v (%v)", state, back, err))
	}
}

func (m Machine[S, M, R, Ev, Ef]) checkCodecs(j func(spec.Rule, spec.Verdict), msg M, d cairn.Decision[R, Ev, Ef]) {
	if m.Codecs == nil {
		return
	}
	j(Encodable, roundTrip(m.Codecs.Messages, msg))
	if !d.Refused() {
		j(Encodable, roundTrip(m.Codecs.Results, d.Result))
	}
	for _, e := range d.Events {
		j(Encodable, roundTrip(m.Codecs.Events, e))
	}
	for _, e := range d.Effects {
		j(Encodable, roundTrip(m.Codecs.Effects, e))
	}
}

func roundTrip[T any](c *codec.Codec[T], v T) spec.Verdict {
	if c == nil {
		return spec.NA
	}
	env, err := c.Encode(v)
	if err != nil {
		return spec.Fail("%v", err)
	}
	// Through bytes, as the ledger stores it.
	raw, err := json.Marshal(env)
	if err != nil {
		return spec.Fail("%v", err)
	}
	var stored codec.Envelope
	if err := json.Unmarshal(raw, &stored); err != nil {
		return spec.Fail("%v", err)
	}
	back, err := c.Decode(stored)
	if err != nil {
		return spec.Fail("%v", err)
	}
	return spec.Check(reflect.DeepEqual(v, back), "%+v came back as %+v", v, back)
}

// describe renders a value with its type's short name, so a trace reads
// "SavePosition{By:ana ...}" rather than an anonymous "{By:ana ...}".
func describe(v any) string {
	name := fmt.Sprintf("%T", v)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name + fmt.Sprintf("%+v", v)
}

func describeAll[T any](vs []T) string {
	if len(vs) == 0 {
		return "no events"
	}
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = describe(v)
	}
	return strings.Join(parts, ", ")
}

// checked runs a rule's check, turning a panic in the check itself into a
// violation of that rule rather than a crash of the whole run.
func checked(check func() spec.Verdict) spec.Verdict {
	v, ok := guard(check)
	if !ok {
		return spec.Fail("the check panicked: %v", v.panic)
	}
	return v.value
}

type guarded[T any] struct {
	value T
	panic any
}

func guard[T any](f func() T) (g guarded[T], ok bool) {
	defer func() {
		if p := recover(); p != nil {
			g.panic, ok = p, false
		}
	}()
	return guarded[T]{value: f()}, true
}

// ---- refinement -------------------------------------------------------------

// Model is a reference model: a second, simpler statement of what the machine
// does, over its own state. It is written without looking at the
// implementation, and the check is that the two agree on every message.
type Model[MS, S, M, R any] struct {
	Rule    spec.Rule
	Initial func() MS
	// Step is the model's answer to a message: its next state, the result it
	// expects, and the refusal it expects (nil to accept). Only refusal codes
	// are compared.
	Step func(model MS, msg M) (MS, R, *cairn.Refusal)
	// Agree is the abstraction function and its comparison in one: Pass when
	// the implementation's state matches the model's.
	Agree func(impl S, model MS) spec.Verdict
	// SameResult compares results. Defaults to reflect.DeepEqual.
	SameResult func(impl, model R) bool
}

// Refinement is a model the runner checks a machine against.
type Refinement[S, M, R any] interface {
	rule() spec.Rule
	start() refinementRun[S, M, R]
}

type refinementRun[S, M, R any] interface {
	agrees(impl S) spec.Verdict
	step(msg M, result R, refusal *cairn.Refusal, after S) []spec.Verdict
}

// Refines turns a model into a refinement check.
func Refines[MS, S, M, R any](m Model[MS, S, M, R]) Refinement[S, M, R] {
	if m.SameResult == nil {
		m.SameResult = func(a, b R) bool { return reflect.DeepEqual(a, b) }
	}
	return model[MS, S, M, R]{m}
}

type model[MS, S, M, R any] struct{ def Model[MS, S, M, R] }

func (m model[MS, S, M, R]) rule() spec.Rule { return m.def.Rule }

func (m model[MS, S, M, R]) start() refinementRun[S, M, R] {
	return &modelRun[MS, S, M, R]{def: m.def, state: m.def.Initial()}
}

type modelRun[MS, S, M, R any] struct {
	def   Model[MS, S, M, R]
	state MS
	// diverged is set at the model's first disagreement. A proposed model's run
	// goes on, but there is no mapping back from the machine's state, so every
	// later comparison would only repeat the first.
	diverged bool
}

func (r *modelRun[MS, S, M, R]) agrees(impl S) spec.Verdict {
	v := r.def.Agree(impl, r.state)
	_, r.diverged = v.Failed()
	return v
}

func (r *modelRun[MS, S, M, R]) step(msg M, result R, refusal *cairn.Refusal, after S) []spec.Verdict {
	if r.diverged {
		return nil
	}
	verdicts := r.compare(msg, result, refusal, after)
	for _, v := range verdicts {
		if _, failed := v.Failed(); failed {
			r.diverged = true
		}
	}
	return verdicts
}

func (r *modelRun[MS, S, M, R]) compare(msg M, result R, refusal *cairn.Refusal, after S) []spec.Verdict {
	next, want, wantRefusal := r.def.Step(r.state, msg)
	code := func(r *cairn.Refusal) string {
		if r == nil {
			return "accepted"
		}
		return "refused " + r.Code
	}
	verdicts := []spec.Verdict{
		spec.Check(code(refusal) == code(wantRefusal), "machine %s, model %s", code(refusal), code(wantRefusal)),
	}
	if refusal == nil && wantRefusal == nil {
		verdicts = append(verdicts, spec.Check(r.def.SameResult(result, want), "machine returned %+v, model %+v", result, want))
	}
	if wantRefusal == nil {
		r.state = next
	}
	return append(verdicts, r.def.Agree(after, r.state))
}

// ---- report -----------------------------------------------------------------

type tally struct {
	rule              spec.Rule
	judged, applied   int
	failures          int
	firstFailure      string
	firstFailureTrace []string
}

type report struct {
	mu    sync.Mutex
	runs  int
	order []string
	rules map[string]*tally
}

func newReport() *report { return &report{rules: map[string]*tally{}} }

func (r *report) ran() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs++
}

func (r *report) declare(rule spec.Rule) {
	if _, ok := r.rules[rule.ID]; ok {
		return
	}
	r.order = append(r.order, rule.ID)
	r.rules[rule.ID] = &tally{rule: rule}
}

func (r *report) count(rule spec.Rule, v spec.Verdict) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.rules[rule.ID]
	t.judged++
	if v.Applies() {
		t.applied++
	}
}

func (r *report) proposed(rule spec.Rule, detail string, trace []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.rules[rule.ID]
	t.failures++
	if t.firstFailure == "" || len(trace) < len(t.firstFailureTrace) {
		t.firstFailure = detail
		t.firstFailureTrace = append([]string(nil), trace...)
	}
}

func (r *report) log(t rapid.TB) {
	t.Helper()
	var b strings.Builder
	b.WriteString("rule health (applied / judged):\n")
	ids := append([]string(nil), r.order...)
	sort.SliceStable(ids, func(i, j int) bool { return r.rules[ids[i]].rule.Status < r.rules[ids[j]].rule.Status })
	for _, id := range ids {
		tl := r.rules[id]
		note := ""
		switch {
		case tl.applied == 0:
			note = "  VACUOUS: never applied"
		case tl.failures > 0:
			note = fmt.Sprintf("  %d violations", tl.failures)
		}
		fmt.Fprintf(&b, "  %-6s %-11s %7d / %-7d%s\n", id, tl.rule.Status, tl.applied, tl.judged, note)
	}
	t.Log(b.String())

	for _, id := range ids {
		tl := r.rules[id]
		if tl.failures == 0 {
			continue
		}
		t.Logf("proposed rule %s would fail: %s\n  rule: %s\n  shortest trace seen:\n    %s",
			id, tl.firstFailure, tl.rule.Statement, strings.Join(tl.firstFailureTrace, "\n    "))
	}
}

// kindsMatch fails t for a rule whose Kind disagrees with how it is checked:
// a model's rule must be Refines, and a law, change or invariant's must not
// be, since nothing compares it with a model.
func (m Machine[S, M, R, Ev, Ef]) kindsMatch(t rapid.TB) bool {
	t.Helper()
	ok := true
	notRefines := func(r spec.Rule, how string) {
		if r.Kind == spec.Refines {
			t.Errorf("%s is checked by %s, not a model, so its Kind cannot be Refines", r.ID, how)
			ok = false
		}
	}
	for _, l := range m.Laws {
		notRefines(l.Rule, "a law")
	}
	for _, c := range m.Changes {
		notRefines(c.Rule, "a change")
	}
	for _, i := range m.Invariants {
		notRefines(i.Rule, "an invariant")
	}
	for _, r := range m.Models {
		if r.rule().Kind != spec.Refines {
			t.Errorf("%s is checked by a model, so its Kind must be Refines", r.rule().ID)
			ok = false
		}
	}
	return ok
}

// gateVacuity fails t for every established rule that never applied, unless
// allowed names it. Proposed rules only report, so their vacuity does too.
func (r *report) gateVacuity(t rapid.TB, allowed []string) {
	t.Helper()
	for _, id := range allowed {
		if _, ok := r.rules[id]; !ok {
			t.Errorf("AllowVacuous names %s, which is not one of the machine's rules", id)
		}
	}
	var vacuous []*tally
	for _, id := range r.order {
		tl := r.rules[id]
		if tl.applied == 0 && tl.rule.Status == spec.Established && !slices.Contains(allowed, id) {
			vacuous = append(vacuous, tl)
		}
	}
	if len(vacuous) == 0 {
		return
	}
	if r.runs < vacuityRuns {
		t.Logf("vacuity not enforced: %d runs, fewer than %d", r.runs, vacuityRuns)
		return
	}
	for _, tl := range vacuous {
		t.Errorf("%s is vacuous: it never applied in %d runs, so the generator never reaches it (list it in AllowVacuous if that is expected)\n  rule: %s",
			tl.rule.ID, r.runs, tl.rule.Statement)
	}
}

// Violation is a proposed rule's first counterexample.
type Violation struct {
	Rule   spec.Rule
	Detail string
	Trace  []string
}

// CheckReport runs Check and also returns the proposed rules that failed, for
// tests that assert a proposed rule is (or is not yet) satisfied.
func (m Machine[S, M, R, Ev, Ef]) CheckReport(t rapid.TB) []Violation {
	t.Helper()
	if m.Equal == nil {
		m.Equal = func(a, b S) bool { return reflect.DeepEqual(a, b) }
	}
	if m.Steps == 0 {
		m.Steps = 40
	}
	if !m.kindsMatch(t) {
		return nil
	}
	rep := newReport()
	for _, r := range m.rules() {
		rep.declare(r)
	}
	rapid.Check(t, func(rt *rapid.T) { m.run(rt, rep) })
	rep.log(t)
	rep.gateVacuity(t, m.AllowVacuous)

	var out []Violation
	for _, id := range rep.order {
		tl := rep.rules[id]
		if tl.failures > 0 {
			out = append(out, Violation{Rule: tl.rule, Detail: tl.firstFailure, Trace: tl.firstFailureTrace})
		}
	}
	return out
}
