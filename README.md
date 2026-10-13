# cairn

An opinionated Go library for state machines that keep a ledger and are
checked against a spec.

The aim is software an agent can change without a human re-verifying every
line. A small core of rules and reference models is owned by humans, and
review attention goes to changes in that core. Everything else is checked
mechanically: against generated inputs, and against the real history in the
ledger.

## Core components

| Package | What it is |
|---|---|
| `cairn` | `Decider`, `Decision`, `Refusal`. Business logic as two pure functions: `Decide` turns a state and a message into events and effects (or a refusal), and `Evolve` folds one event into the state. |
| `codec` | Versioned JSON for messages, events and effects, with upcasters for old versions. |
| `ledger` | The Postgres shell. It takes per-stream locks, appends every decided message (refusals included) to an append-only log, queues effects in an outbox in the same transaction, runs projections over the global log, and re-decides stored history with `Verify`. |
| `spec` / `spectest` | Rules (`Never` / `Can` / `Refines`, `Established` / `Proposed`) and a rapid-based checker. It holds every machine to the framework's own rules (deterministic, total, refusals carry nothing, no mutation of the given state, replayable) as well as to the machine's rules, and reports how often each rule actually applied. |
| `blocks/fsm` | A lifecycle as a table of allowed transitions. `Decide` consults it, the spec enforces it, and review renders it. |
| `ledger/ledgertest` | A fresh, migrated database for each test. |
| `examples/decisions` | lore's decision register rebuilt on cairn, end to end. |

## A short demo

A bank account that refuses to go overdrawn, and warns when the balance runs
low.

```go
type Account struct{ Balance int }

type Deposit struct{ Amount int }    // messages
type Withdraw struct{ Amount int }
type Deposited struct{ Amount int }  // events
type Withdrew struct{ Amount int }
type WarnLowBalance struct{ Balance int } // an effect

var Bank = cairn.Decider[Account, Msg, int, Event, Effect]{
	Initial: func() Account { return Account{} },
	Decide: func(a Account, m Msg) cairn.Decision[int, Event, Effect] {
		switch m := m.(type) {
		case Deposit:
			if m.Amount <= 0 {
				return cairn.Refuse[int, Event, Effect]("invalid", "deposit of %d", m.Amount)
			}
			return cairn.Accept[int, Event, Effect](a.Balance+m.Amount, []Event{Deposited{m.Amount}})
		case Withdraw:
			if m.Amount <= 0 || m.Amount > a.Balance {
				return cairn.Refuse[int, Event, Effect]("insufficient", "%d of %d", m.Amount, a.Balance)
			}
			left := a.Balance - m.Amount
			var effects []Effect
			if left < 10 {
				effects = append(effects, WarnLowBalance{left})
			}
			return cairn.Accept(left, []Event{Withdrew{m.Amount}}, effects...)
		}
		return cairn.Refuse[int, Event, Effect]("unknown", "%T", m)
	},
	Evolve: func(a Account, e Event) Account {
		switch e := e.(type) {
		case Deposited:
			a.Balance += e.Amount
		case Withdrew:
			a.Balance -= e.Amount
		}
		return a
	},
}
```

(`Msg`, `Event` and `Effect` are marker interfaces that the types above
implement.)

**The spec.** A rule a human owns, checked against generated message
sequences, alongside the framework's own rules:

```go
var NeverOverdrawn = spec.Rule{ID: "B1", Kind: spec.Never, Status: spec.Established,
	Statement: "An account's balance goes below zero."}

func TestBankSpec(t *testing.T) {
	spectest.Machine[Account, Msg, int, Event, Effect]{
		Decider: Bank,
		Messages: func(t *rapid.T, a Account) Msg {
			n := rapid.IntRange(-5, a.Balance+20).Draw(t, "amount")
			if rapid.Bool().Draw(t, "deposit") {
				return Deposit{n}
			}
			return Withdraw{n}
		},
		Invariants: []spec.Invariant[Account]{{Rule: NeverOverdrawn, Check: func(a Account) spec.Verdict {
			return spec.Check(a.Balance >= 0, "balance is %d", a.Balance)
		}}},
	}.Check(t)
}
```

**The ledger.** The same decider, persisted:

```go
store := ledger.New(pool)
if err := store.Migrate(ctx); err != nil { ... }

accounts := &ledger.Aggregate[Account, Msg, int, Event, Effect]{
	Store: store, Kind: "account", Decider: Bank,
	Messages: msgs, Events: events, Effects: effects, // codecs
}

accounts.Handle(ctx, "alice", Deposit{50})
accounts.Handle(ctx, "alice", Withdraw{80}) // refused, and the refusal is recorded
accounts.Handle(ctx, "alice", Withdraw{45}) // balance 5; WarnLowBalance goes to the outbox

divergences, err := accounts.VerifyAll(ctx) // would today's code decide the same?
```

## What the ledger guarantees

- **One transaction per message.** A decided message, its events, its effects,
  its snapshot and its views commit together, or none of them do.
- **Read your own writes.** An aggregate's `Views` are written from the
  stream's new state inside that transaction, so a caller that writes and then
  reads a view table sees its write. A view replaces a stream's rows from its
  whole state, which makes it safe to run again: `Aggregate.Rebuild` fills a
  new or changed view from current state, while messages keep arriving.
  Read models that combine streams use a projection instead, which is
  eventually consistent.
- **No interleaving within a stream.** `Tx.Lock` takes every stream lock a
  transaction needs in one sorted call, so overlapping transactions cannot
  deadlock. Locking a stream doesn't create it: a stream exists once its first
  entry is written, so one locked only to be read leaves nothing behind.
- **Append-only entries,** enforced by a trigger. A CHECK constraint keeps
  refusals free of events and effects.
- **No gaps for readers of the global log.** `ReadAll` and projections hold
  back entries from transactions that are still in flight until every older
  transaction has finished, so a reader never skips past an entry that
  commits late.
- **Safe retries.** `IdempotencyKey` returns the first answer, refusals
  included.
- **Replay verification.** `Aggregate.Verify` re-decides every stored message
  with today's code and reports each divergence. State between messages comes
  from the stored events, so one divergence doesn't cascade.

The library owns the `cairn` schema. Applications read from it through this
package and never write to it directly.

## What the checker guarantees

Every machine is held to the framework's rules (D1–D7) in addition to its own:

| Rule | Statement |
|---|---|
| D1 | Decide is deterministic. |
| D2 | Decide and Evolve never panic. |
| D3 | A refusal carries no events or effects. |
| D4 | Decide and Evolve never modify the state they're given. This catches a map shared between states. |
| D5 | Folding a run's events from the initial state reproduces the run. |
| D6 | Messages, events and effects survive their codecs (when codecs are given). |
| D7 | State survives a JSON snapshot round trip (when `Snapshots` is set). |

Established rules fail the test with a shrunk counterexample. Proposed rules
are reported with the shortest trace found. The rule-health table shows how
often each rule actually applied. An established rule that never applies
fails the check as vacuous, because a check that is never exercised proves
nothing: make the generator reach it, or list it in `AllowVacuous` if it
genuinely can't apply to this machine. Vacuity is only enforced on a
full-length check (rapid's default 100 runs). Under `-short` or a lowered
`-rapid.checks` it is only reported, since a rarely applied rule can miss a
short check by chance.

## The spec manifest

[`spec.manifest.yaml`](spec.manifest.yaml) lists every rule, every type
stored in the ledger (its codec name, version, upcasters and JSON shape), and
every fsm table. It's generated from the code and committed, so a review
reads a change to the spec as a diff of that file: a rule added, demoted or
reworded, an edge added to a lifecycle, a field added to an event.

```sh
go -C cmd/cairn-manifest run . -dir ../..
```

`cmd/cairn-manifest` reads the source statically, so it sees everything that
is declared, whether or not a test uses it. It reads:

- **Rules:** package-level `spec.Rule` variables, test files included, since
  a machine's rules usually live with its spec test.
- **Tables:** package-level variables built by `fsm.New`.
- **Codecs:** `codec.Register` and `Upcast` calls outside test files, since
  only production code writes to the ledger.

Each of these must be stated with constants. A rule, table or registration
the tool can't read exactly is an error, not a guess. Declare a fixture
rule inside a function to keep it out of the manifest.

CI fails when the manifest is stale. On a pull request it also compares the
codecs with the base branch's manifest and fails if a stored type was
removed, went back a version, changed shape without a version bump, or was
bumped without an upcaster from each earlier version. Any of these would
leave entries already in the ledger unreadable.

## Working on cairn

```sh
export CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
go test ./...
```

Without the database URL, ledger tests are skipped. In CI, set
`CAIRN_REQUIRE_DATABASE=1` as well, so they fail instead of skipping.

The manifest tool is its own module, so the library doesn't depend on
`golang.org/x/tools`. Its tests also check that the committed manifest is
current:

```sh
go -C cmd/cairn-manifest test ./...
```

See [docs/ROADMAP.md](docs/ROADMAP.md) for what's next and the open questions.
