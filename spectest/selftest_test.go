package spectest_test

// The checker is what every machine's trust rests on, so it is held to the
// same standard as the machines: a correct machine passes, and every way of
// breaking a framework rule or a model is caught by that rule and no other.

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spec"
	"github.com/anyweez/cairn/spectest"
)

// account is the fixture's state. Its reference fields exist so mutants have
// something to alias: Moves through a slice, Counts through a map, Extra
// through an interface.
type account struct {
	Balance int
	Moves   []int
	Counts  map[string]int
	Extra   any
	// Note is never set by the correct machine. It is not snapshotted, so a
	// machine that sets it cannot be restored from a snapshot.
	Note string `json:"-"`
}

type msg interface{ isMsg() }

type deposit struct {
	N int
	// Memo is dropped by encoding. The correct generator leaves it empty.
	Memo string `json:"-"`
}
type withdraw struct{ N int }

func (deposit) isMsg()  {}
func (withdraw) isMsg() {}

type event interface{ isEvent() }

type deposited struct{ N int }
type withdrew struct{ N int }

func (deposited) isEvent() {}
func (withdrew) isEvent()  {}

type effect interface{ isEffect() }

type lowBalance struct{ Balance int }

func (lowBalance) isEffect() {}

type (
	acct     = spectest.Machine[account, msg, int, event, effect]
	decision = cairn.Decision[int, event, effect]
)

func accept(balance int, events []event, effects ...effect) decision {
	return cairn.Accept(balance, events, effects...)
}

func refuse(code string) decision { return cairn.Refuse[int, event, effect](code, "refused") }

func decide(a account, m msg) decision {
	switch m := m.(type) {
	case deposit:
		if m.N <= 0 {
			return refuse("invalid")
		}
		return accept(a.Balance+m.N, []event{deposited{m.N}})
	case withdraw:
		if m.N <= 0 || m.N > a.Balance {
			return refuse("insufficient")
		}
		left := a.Balance - m.N
		var effects []effect
		if left < 5 {
			effects = append(effects, lowBalance{left})
		}
		return accept(left, []event{withdrew{m.N}}, effects...)
	}
	return refuse("unknown")
}

// evolve copies every reference it changes, which is what keeps Decide and
// Evolve from modifying the state they were given.
func evolve(a account, e event) account {
	counts := maps.Clone(a.Counts)
	if counts == nil {
		counts = map[string]int{}
	}
	switch e := e.(type) {
	case deposited:
		a.Balance += e.N
		a.Moves = append(slices.Clone(a.Moves), e.N)
		counts["deposit"]++
	case withdrew:
		a.Balance -= e.N
		a.Moves = append(slices.Clone(a.Moves), -e.N)
		counts["withdraw"]++
	}
	a.Counts = counts
	return a
}

// messages draws amounts past the balance and below one, so both refusals
// turn up, and exactly the balance, so a withdrawal can empty the account.
func messages(t *rapid.T, a account) msg {
	n := rapid.IntRange(-2, a.Balance+5).Draw(t, "n")
	if rapid.Bool().Draw(t, "deposit") {
		return deposit{N: n}
	}
	if rapid.IntRange(0, 4).Draw(t, "exact") == 0 {
		n = a.Balance
	}
	return withdraw{N: n}
}

func codecs() *spectest.Codecs[msg, int, event, effect] {
	msgs := codec.New[msg]()
	codec.Register[deposit](msgs, "deposit", 1)
	codec.Register[withdraw](msgs, "withdraw", 1)
	events := codec.New[event]()
	codec.Register[deposited](events, "deposited", 1)
	codec.Register[withdrew](events, "withdrew", 1)
	effects := codec.New[effect]()
	codec.Register[lowBalance](effects, "low_balance", 1)
	results := codec.New[int]()
	codec.Register[int](results, "balance", 1)
	return &spectest.Codecs[msg, int, event, effect]{Messages: msgs, Results: results, Events: events, Effects: effects}
}

// The fixture's own rules, built in functions because they are not cairn's:
// the spec manifest lists package-level rules only.

func neverOverdrawn() spec.Rule {
	return spec.Rule{ID: "A1", Kind: spec.Never, Status: spec.Established,
		Statement: "An account's balance goes below zero."}
}

func refinesLedger() spec.Rule {
	return spec.Rule{ID: "A2", Kind: spec.Refines, Status: spec.Established,
		Statement: "The account agrees with a running total that refuses overdrafts."}
}

// ledgerModel is the account restated as a bare running total.
func ledgerModel() spectest.Model[int, account, msg, int] {
	return spectest.Model[int, account, msg, int]{
		Rule:    refinesLedger(),
		Initial: func() int { return 0 },
		Step: func(total int, m msg) (int, int, *cairn.Refusal) {
			switch m := m.(type) {
			case deposit:
				if m.N <= 0 {
					return total, 0, &cairn.Refusal{Code: "invalid"}
				}
				return total + m.N, total + m.N, nil
			case withdraw:
				if m.N <= 0 || m.N > total {
					return total, 0, &cairn.Refusal{Code: "insufficient"}
				}
				return total - m.N, total - m.N, nil
			}
			return total, 0, &cairn.Refusal{Code: "unknown"}
		},
		Agree: func(a account, total int) spec.Verdict {
			return spec.Check(a.Balance == total, "balance %d, model %d", a.Balance, total)
		},
	}
}

func machine() acct {
	return acct{
		Decider: cairn.Decider[account, msg, int, event, effect]{
			Initial: func() account { return account{} },
			Decide:  decide,
			Evolve:  evolve,
		},
		Messages: messages,
		Invariants: []spec.Invariant[account]{{Rule: neverOverdrawn(), Check: func(a account) spec.Verdict {
			return spec.Check(a.Balance >= 0, "balance is %d", a.Balance)
		}}},
		Models:    []spectest.Refinement[account, msg, int]{spectest.Refines(ledgerModel())},
		Codecs:    codecs(),
		Snapshots: true,
	}
}

func TestCorrectMachinePasses(t *testing.T) {
	if msg, failed := spectest.Fails(t, machine().Check); failed {
		t.Fatalf("the correct machine failed: %s", msg)
	}
}

// Each mutant breaks one rule, and must be caught by that rule.
func TestFrameworkRulesCatchMutants(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(m *acct)
		rule   string
		detail string
	}{
		{
			name: "Decide answers differently on a second call",
			mutate: func(m *acct) {
				calls := 0
				m.Decider.Decide = func(a account, msg msg) decision {
					calls++
					d := decide(a, msg)
					d.Result += calls % 2
					return d
				}
			},
			rule: "D1",
		},
		{
			name: "Decide panics instead of refusing",
			mutate: func(m *acct) {
				m.Decider.Decide = func(a account, msg msg) decision {
					if w, ok := msg.(withdraw); ok && w.N > a.Balance {
						panic("overdrawn")
					}
					return decide(a, msg)
				}
			},
			rule: "D2", detail: "Decide panicked",
		},
		{
			name: "Decide panics only when called again",
			mutate: func(m *acct) {
				calls := 0
				m.Decider.Decide = func(a account, msg msg) decision {
					calls++
					if calls%2 == 0 {
						panic("second call")
					}
					return decide(a, msg)
				}
			},
			rule: "D2", detail: "second call",
		},
		{
			name: "Evolve panics when an account empties",
			mutate: func(m *acct) {
				m.Decider.Evolve = func(a account, e event) account {
					if w, ok := e.(withdrew); ok && w.N == a.Balance {
						panic("empty")
					}
					return evolve(a, e)
				}
			},
			rule: "D2", detail: "Evolve panicked",
		},
		{
			name:   "Initial panics",
			mutate: func(m *acct) { m.Decider.Initial = func() account { panic("no account") } },
			rule:   "D2", detail: "Initial panicked",
		},
		{
			name: "a refusal carries the event it refused",
			mutate: func(m *acct) {
				m.Decider.Decide = func(a account, msg msg) decision {
					d := decide(a, msg)
					if d.Refused() {
						d.Events = []event{withdrew{1}}
					}
					return d
				}
			},
			rule: "D3",
		},
		{
			name: "Decide rewrites a slice element it shares with its state",
			mutate: func(m *acct) {
				m.Decider.Decide = func(a account, msg msg) decision {
					if len(a.Moves) > 0 {
						a.Moves[0] = 0
					}
					return decide(a, msg)
				}
			},
			rule: "D4", detail: "Decide modified",
		},
		{
			name: "Evolve writes into a map it shares with its state",
			mutate: func(m *acct) {
				m.Decider.Evolve = func(a account, e event) account {
					if a.Counts == nil {
						a.Counts = map[string]int{}
					}
					a.Counts["event"]++
					return evolve(a, e)
				}
			},
			rule: "D4", detail: "Evolve modified",
		},
		{
			name: "Evolve appends into the slice it shares with its state",
			mutate: func(m *acct) {
				m.Decider.Evolve = func(a account, e event) account {
					n := 0
					switch e := e.(type) {
					case deposited:
						n = e.N
					case withdrew:
						n = -e.N
					}
					next := evolve(a, e)
					next.Moves = append(a.Moves, n)
					return next
				}
			},
			rule: "D4", detail: "Evolve modified",
		},
		{
			name: "Decide writes into a map behind an interface",
			mutate: func(m *acct) {
				m.Decider.Initial = func() account { return account{Extra: map[string]int{}} }
				m.Decider.Decide = func(a account, msg msg) decision {
					a.Extra.(map[string]int)["seen"]++
					return decide(a, msg)
				}
				// A map in an interface comes back from JSON as map[string]any.
				m.Snapshots = false
			},
			rule: "D4", detail: "Decide modified",
		},
		{
			name: "Evolve depends on something outside its inputs",
			mutate: func(m *acct) {
				calls := 0
				m.Decider.Evolve = func(a account, e event) account {
					calls++
					a = evolve(a, e)
					a.Moves[len(a.Moves)-1] = calls
					return a
				}
			},
			rule: "D5",
		},
		{
			name: "a message field is lost in encoding",
			mutate: func(m *acct) {
				m.Messages = func(t *rapid.T, a account) msg {
					if d, ok := messages(t, a).(deposit); ok {
						d.Memo = "rent"
						return d
					}
					return withdraw{N: 1}
				}
			},
			rule: "D6",
		},
		{
			name: "a state field is lost in a snapshot",
			mutate: func(m *acct) {
				m.Decider.Evolve = func(a account, e event) account {
					a = evolve(a, e)
					a.Note = "touched"
					return a
				}
			},
			rule: "D7",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := machine()
			c.mutate(&m)
			assertCaught(t, m, c.rule, c.detail)
		})
	}
}

// A model that disagrees with the machine in any of the three ways it can is
// caught by the model's rule.
func TestRefinementCatchesDisagreement(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(model *spectest.Model[int, account, msg, int])
		detail string
	}{
		{
			name: "on whether to refuse",
			mutate: func(model *spectest.Model[int, account, msg, int]) {
				step := model.Step
				model.Step = func(total int, m msg) (int, int, *cairn.Refusal) {
					if w, ok := m.(withdraw); ok && w.N > total {
						return total - w.N, total - w.N, nil
					}
					return step(total, m)
				}
			},
			detail: "model accepted",
		},
		{
			name: "on the result",
			mutate: func(model *spectest.Model[int, account, msg, int]) {
				step := model.Step
				model.Step = func(total int, m msg) (int, int, *cairn.Refusal) {
					next, result, refusal := step(total, m)
					return next, result + 1, refusal
				}
			},
			detail: "machine returned",
		},
		{
			name: "on the state",
			mutate: func(model *spectest.Model[int, account, msg, int]) {
				step := model.Step
				model.Step = func(total int, m msg) (int, int, *cairn.Refusal) {
					next, result, refusal := step(total, m)
					if _, ok := m.(deposit); ok && refusal == nil {
						next++
					}
					return next, result, refusal
				}
			},
			detail: "balance",
		},
		{
			name: "from the start",
			mutate: func(model *spectest.Model[int, account, msg, int]) {
				model.Initial = func() int { return 1 }
			},
			detail: "model 1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			model := ledgerModel()
			c.mutate(&model)
			m := machine()
			m.Models = []spectest.Refinement[account, msg, int]{spectest.Refines(model)}
			assertCaught(t, m, refinesLedger().ID, c.detail)
		})
	}
}

// A proposed model reports its first disagreement and does not fail the check.
func TestProposedModelReportsWithoutFailing(t *testing.T) {
	model := ledgerModel()
	model.Rule.Status = spec.Proposed
	model.Initial = func() int { return 1 }
	m := machine()
	m.Models = []spectest.Refinement[account, msg, int]{spectest.Refines(model)}

	var violations []spectest.Violation
	if msg, failed := spectest.Fails(t, func(t rapid.TB) { violations = m.CheckReport(t) }); failed {
		t.Fatalf("a proposed model failed the check: %s", msg)
	}
	if len(violations) != 1 || violations[0].Rule.ID != refinesLedger().ID {
		t.Fatalf("want one violation of %s, got %+v", refinesLedger().ID, violations)
	}
	if !strings.Contains(violations[0].Detail, "model 1") {
		t.Fatalf("the violation should be the first disagreement, got %q", violations[0].Detail)
	}
}

// An invariant breaks without the model noticing when the model shares the
// bug, so the invariant is checked on its own.
func TestInvariantCatchesOverdraft(t *testing.T) {
	m := machine()
	m.Models = nil
	m.Decider.Decide = func(a account, msg msg) decision {
		if w, ok := msg.(withdraw); ok && w.N > 0 {
			return accept(a.Balance-w.N, []event{withdrew{w.N}})
		}
		return decide(a, msg)
	}
	assertCaught(t, m, neverOverdrawn().ID, "balance is -")
}

func assertCaught(t *testing.T, m acct, rule, detail string) {
	t.Helper()
	msg, failed := spectest.Fails(t, m.Check)
	if !failed {
		t.Fatalf("the mutant survived; %s should have caught it", rule)
	}
	if !strings.Contains(msg, rule+" violated") {
		t.Fatalf("want a %s violation, got:\n%s", rule, msg)
	}
	if detail != "" && !strings.Contains(msg, detail) {
		t.Fatalf("want %q in the %s failure, got:\n%s", detail, rule, msg)
	}
}
