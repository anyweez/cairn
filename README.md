<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/cairn-logo-dark.svg">
    <img alt="cairn" src="docs/assets/cairn-logo-light.svg" height="64">
  </picture>
</h1>

An opinionated Go library for state machines that keep a ledger and are
checked against a spec.

The aim is software an agent can change without a human re-verifying every
line. A small core of rules and reference models is owned by humans, and
review attention goes to changes in that core. Everything else is checked
mechanically: against generated inputs, and against the real history in the
ledger.

## When to use cairn

Use cairn when **whether a write is allowed depends on the writes that came
before it**: anywhere a correct-looking request can be refused because of
what already happened.

This is the most bug-prone code in an application, which is why it is worth
covering. The same shapes of bug recur, they are easy for an agent to
introduce, and they are hard to spot in review:

- check-then-act races;
- a side effect performed before the decision is recorded;
- the same rule copied into two places that drift apart;
- rules that live only in the UI;
- lifecycles with no transition table, so any status can become any other;
- concurrent edits where one silently overwrites the other.

cairn rules each of these out by construction, or turns it into a rule that
a human writes once and the checker enforces from then on.

For a new subsystem, these questions decide it:

1. **Can a valid-looking request be refused because of current state?** If
   nothing is ever refused, cairn adds cost without adding trust.
2. **Do you own the truth?** If an external system is authoritative, your
   copy is a mirror. Use cairn for your decisions about that system ("merge
   this pull request"), not for the mirror itself.
3. **Is each entity's write rate modest, and does its history matter?**
   Every entity is a stream, and streams only grow.
4. **Would you want to re-run history against new code?** `Verify` is much
   of the value.
5. **Must a change cause something elsewhere, neither lost nor doubled?**
   The outbox is reason enough, even when the rules are simple.

Examples:

- **In a web app:** orders, subscriptions and billing state, approvals and
  workflows, bookings and reservations, membership and permission grants,
  moderation, quotas, invitations.
- **In a game server:** inventory, trades, currency, auctions, quest
  progress, guild membership, rewards.

Most subsystems are a decision core inside a CRUD shell. An issue's
lifecycle belongs in cairn, and its comment text does not. Draw the line
inside the subsystem, not around it.

**Personal data.** Messages, events, effects, results and metadata must
carry opaque identifiers, never personal data — cairn stores whatever it is
given and can never delete it. The decision *about* the data belongs in
cairn; the data itself stays in your own mutable tables, where ordinary
deletion works. See [docs/REDACTION.md](docs/REDACTION.md).

**Refusals.** A refused message is an entry like any other, so `Verify` can
replay it and a retried refusal gets the same answer. That means a caller
free to send without limit can grow a stream without limit. The library does
not rate-limit refusals or keep them in a separate table; bound abusive
traffic at the request edge, per actor or per stream. See
[docs/REFUSALS.md](docs/REFUSALS.md).

## When not to use cairn

- **CRUD with no rules:** profile fields, content bodies, settings.
- **Telemetry, metrics and logs:** high volume, nothing to decide.
- **Caches and derived data:** a projection's job, not a stream's.
- **Mirrors of an external system**, where that system is the source of
  truth.
- **The value of a session, token or other secret, or any personal data.**
  Entries are append-only and are never redacted, so content stays in your
  own mutable tables. A decision *about* it — issue, expire, revoke —
  belongs in cairn, named by an opaque id. See
  [docs/REDACTION.md](docs/REDACTION.md).
- **High-frequency simulation:** game ticks, movement, physics, AI.

For simulation, the rule is that **the simulation proposes and the ledger
decides**. The tick loop stays in memory and fast. What it produces that
must persist and be fair ("award this loot", "grant this XP") becomes a
message to a stream, which can refuse it.

The parts of a system that cairn doesn't cover still need a contract, just a
different kind:

| Kind of subsystem | Contract |
|---|---|
| Decisions over state you own | a cairn spec, plus `Verify` against history |
| CRUD | schema and API contract tests |
| Integrations | contract tests against the external API |
| Simulation | deterministic replay and property tests |
| UI | agent-driven or visual checks |
| Telemetry | usually none |

## Core components

| Package | What it is |
|---|---|
| `cairn` | `Decider`, `Decision`, `Refusal`. Business logic as two pure functions: `Decide` turns a state and a message into events and effects (or a refusal), and `Evolve` folds one event into the state. |
| `codec` | Versioned JSON for messages, results, events and effects, with upcasters for old versions. |
| `ledger` | The Postgres shell. It takes per-stream locks, appends every decided message it can store (refusals included) to an append-only log, queues effects in an outbox in the same transaction, runs projections over the global log, and re-decides stored history with `Verify`. |
| `spec` / `spectest` | Rules (`Never` / `Can` / `Refines`, `Established` / `Proposed`), checked as laws over a transition, changes over the state alone, invariants over every state, and refinements of a reference model, by a rapid-based checker. It holds every machine to the framework's own rules (deterministic, total, refusals carry nothing, no mutation of the given state, replayable) as well as to the machine's rules, and reports how often each rule actually applied. |
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

// Go can't infer a Decision's type parameters from a return, so a machine
// names them once, in an alias and two helpers.
type decision = cairn.Decision[int, Event, Effect]

func refuse(code, format string, args ...any) decision {
	return cairn.Refuse[int, Event, Effect](code, format, args...)
}
func accept(balance int, events []Event, effects ...Effect) decision {
	return cairn.Accept(balance, events, effects...)
}

var Bank = cairn.Decider[Account, Msg, int, Event, Effect]{
	Initial: func() Account { return Account{} },
	Decide: func(a Account, m Msg) decision {
		switch m := m.(type) {
		case Deposit:
			if m.Amount <= 0 {
				return refuse("invalid", "deposit of %d", m.Amount)
			}
			return accept(a.Balance+m.Amount, []Event{Deposited{m.Amount}})
		case Withdraw:
			if m.Amount <= 0 || m.Amount > a.Balance {
				return refuse("insufficient", "%d of %d", m.Amount, a.Balance)
			}
			left := a.Balance - m.Amount
			var effects []Effect
			if left < 10 {
				effects = append(effects, WarnLowBalance{left})
			}
			return accept(left, []Event{Withdrew{m.Amount}}, effects...)
		}
		return refuse("unknown", "%T", m)
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
		Codecs:  Codecs(), // D6: every value survives its codec
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

**The ledger.** The same decider, persisted. Every type the ledger stores is
registered under a name and a version that never change, and the four codecs
travel together as a `codec.Set`, which the spec test round-trips through too
(D6):

```go
func Codecs() *codec.Set[Msg, int, Event, Effect] {
	msgs := codec.New[Msg]()
	codec.Register[Deposit](msgs, "account.deposit", 1)
	codec.Register[Withdraw](msgs, "account.withdraw", 1)
	events := codec.New[Event]()
	codec.Register[Deposited](events, "account.deposited", 1)
	codec.Register[Withdrew](events, "account.withdrew", 1)
	effects := codec.New[Effect]()
	codec.Register[WarnLowBalance](effects, "account.warn_low_balance", 1)
	results := codec.New[int]()
	codec.Register[int](results, "account.balance", 1)
	return &codec.Set[Msg, int, Event, Effect]{Messages: msgs, Results: results, Events: events, Effects: effects}
}
```

```go
store := ledger.New(pool)
if err := store.Migrate(ctx); err != nil { ... }

accounts := &ledger.Aggregate[Account, Msg, int, Event, Effect]{
	Store: store, Kind: "account", Decider: Bank, Codecs: Codecs(),
}
if err := accounts.Validate(); err != nil { ... } // every method checks this too

// Usually set once per request, by middleware. Every entry needs an actor.
ctx = ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_alice", Via: "api", CorrelationID: requestID})

accounts.Handle(ctx, "alice", Deposit{50})
accounts.Handle(ctx, "alice", Withdraw{80}) // refused, and the refusal is recorded
accounts.Handle(ctx, "alice", Withdraw{45}) // balance 5; WarnLowBalance goes to the outbox

divergences, err := accounts.VerifyAll(ctx) // would today's code decide the same?
```

## What the ledger guarantees

- **Atomic messages.** A decided message, its events, its effects, its
  snapshot and its views commit together, or none of them do. Messages that
  arrive together for one stream may share a transaction; each caller returns
  once its own message has committed.
- **Effects are delivered at least once, in no guaranteed order.** An effect
  is queued in the transaction that records it and carried out by a
  dispatcher, which claims it with a lease and retries a failure. A handler
  must be idempotent and must not depend on delivery order; `Delivery.Key()`
  names an effect across retries. See [docs/EFFECTS.md](docs/EFFECTS.md).
- **Snapshots are a fold, and the version names it.** A snapshot holds the
  state `Evolve` produced at a seq, and it is reused only under the
  `StateVersion` that took it. That version must change when `Evolve` changes
  how it folds events, not only when the state type changes: otherwise a
  stream that has a snapshot keeps the old fold, while a stream rebuilt from
  the same events gets the new one. A snapshot is stored as the bytes
  `json.Marshal` wrote, so a load returns exactly the state that was
  snapshotted; a state that could not be read back unchanged — text
  `json.Marshal` would rewrite, or a NUL — is refused with a cairn error when
  the snapshot is due, rather than stored as something else.
- **Read your own writes.** An aggregate's `Views` are written from the
  stream's new state inside that transaction, so a caller that writes and then
  reads a view table sees its write. A view replaces a stream's rows from its
  whole state, which makes it safe to run again: `Aggregate.Rebuild` fills a
  new or changed view from current state, while messages keep arriving.
  Read models that combine streams use a projection instead, which is
  eventually consistent. A view is also handed the entry that caused the
  write, for display columns like `updated_by`. Views queue their statements
  in a `Batch` rather than running them, so they go out with the message's
  own writes and add no round trip (see [docs/CONVENTIONS.md](docs/CONVENTIONS.md)).
- **History in the same transaction.** A view is written once per
  transaction, so it can't keep a row per entry. An aggregate's `EntryViews`
  can: each is called once for every entry with events, with the state before
  and after it and its events, and writes in the same batch, after the views.
  That fits a timeline or an audit trail the page shows as soon as the write
  returns. Rows keyed by the entry make it safe to run again, and
  `Aggregate.RebuildEntries` replays every entry through it.
- **Every entry is attributed.** Each entry records who sent the message
  (`Actor`), through what (`Via`), the flow it belongs to (`CorrelationID`),
  what directly caused it (`CausationID`) and when the producer says it
  happened (`OccurredAt`). Metadata comes from the context, set once per
  request, or `WithMeta` for one message. A message with no actor is refused
  unless the store is built with `RequireActor(false)`. Set `Aggregate.Actor`
  when messages name their own sender, and a request whose metadata names
  someone else is refused. A message recorded while carrying out an effect is
  caused by that delivery, in the same flow. `Decide` never sees metadata:
  what a decision depends on belongs in the message, where `Verify` can
  replay it.
- **No interleaving within a stream.** `Transact` takes every stream lock a
  transaction needs in one sorted call, before anything else, so overlapping
  transactions cannot deadlock. Locking a stream doesn't create it: a stream exists once its first
  entry is written, so one locked only to be read leaves nothing behind.
- **Append-only entries,** enforced by a trigger. A CHECK constraint keeps
  refusals free of events and effects. Every entry column is stored as the
  bytes `json.Marshal` wrote, so a retry, `ReadStream` and `Verify` see the
  message, result, events, effects and metadata the decider produced rather
  than a re-serialised version of them.
- **No gaps for readers of the global log.** `ReadAll` and projections hold
  back entries from transactions that are still in flight until every older
  transaction has finished, so a reader never skips past an entry that
  commits late.
- **Safe retries.** `IdempotencyKey` returns the first answer, refusals
  included. A stored result is decoded through the `Results` codec's
  upcasters, so a retry gets the current shape; a field added since it was
  recorded comes back as its zero value unless the upcaster fills it in. A
  request that names an actor or a stream the ledger would refuse is refused
  rather than answered: a key never stands in for an identity check.
- **Told when a message was recorded.** Every statement in a write batch runs
  before the caller reads any result, so a handler can fail in Go — a scan or
  a parse — after the transaction has committed. And even when every result is
  read, closing the batch on a lost connection fails after the commit has
  happened. Either error wraps `ErrRecorded`, which says the message is in the
  ledger even though the caller cannot be given its result. A caller must not
  replay it unless it carries an idempotency key.
- **Replay verification.** `Aggregate.Verify` re-decides every stored message
  with today's code and reports each divergence. State between messages comes
  from the stored events, so one divergence doesn't cascade. A stored result
  is upcast and compared on the fields it carries, so a field added to the
  result since an entry was recorded is checked only when its upcaster can
  fill it in.

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
| D6 | Messages, accepted results, events and effects survive their codecs (when codecs are given). |
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
the tool can't read exactly is an error, not a guess. So is a registration
inside a generic helper: its type argument is the helper's parameter, not the
type the caller supplies, so the tool refuses it — register the concrete type
where the helper is instantiated. And so is a module it can't load at all — a
`go.mod` that needs tidying, say: the tool fails with go's own message rather
than writing a manifest that says the module declares nothing. Declare a
fixture rule inside a function to keep it out of the manifest.

Reading codecs from source can still be fooled: an `Upcast` inside a helper
that never runs looks like an upcaster, and a `Register` taken as a function
value is invisible. A test closes that gap by building the module's real
codecs and comparing them with the committed manifest. `codec.Registrations`
reports what a codec holds, and `codec.CheckRegistrations` reports every
difference from a manifest; `examples/decisions/manifest_test.go` is the
example. A mismatch fails the gate before an entry already in the ledger
would fail to decode.

CI fails when the manifest is stale. On a pull request it also compares the
codecs with the base branch's manifest and fails if a stored type was
removed, went back a version, changed shape without a version bump, or was
bumped without an upcaster from each earlier version. Any of these would
leave entries already in the ledger unreadable.

## Performance

`Handle` costs two round trips to Postgres, and one stream's lock is held for
about one and a half of them, so throughput depends mostly on the link's
latency and the connection pool's size. Set the pool size explicitly:
pgxpool's default of 4 connections caps throughput long before anything else.
A store also keeps recently used streams' state in memory, so a message to a
cached stream costs one round trip. Messages that arrive together for one
stream are recorded in one transaction. That pays most when each stream's
traffic reaches one process. When it doesn't, the cache notices, and costs
about what no cache would.
[docs/PERFORMANCE.md](docs/PERFORMANCE.md) explains how to size a deployment,
and [docs/BENCHMARKS.md](docs/BENCHMARKS.md) has the measurements.

## Working on cairn

```sh
export CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
go test ./...
```

Without the database URL, ledger tests are skipped. Set
`CAIRN_REQUIRE_DATABASE=1` as well to make a missing database fail instead of
skip. It also makes the prepared-transaction test fail when the server has
`max_prepared_transactions=0`, because the gate must not pass without
exercising that fence; `scripts/test-postgres.sh` sets it above 0.

The manifest tool is its own module, so the library doesn't depend on
`golang.org/x/tools`. Its tests also check that the committed manifest is
current:

```sh
go -C cmd/cairn-manifest test ./...
```

To run the ledger tests the way a pooled deployment connects, through PgBouncer
in transaction mode:

```sh
export CAIRN_TEST_DATABASE_URL=$(scripts/test-pgbouncer.sh)
go test ./ledger/... ./examples/...
```

Releases are `vX.Y.Z` tags, with the version and notes worked out from the
commit messages; [docs/RELEASING.md](docs/RELEASING.md) has the procedure.
Pin a version with `go get github.com/anyweez/cairn@vX.Y.Z`.

See [docs/ROADMAP.md](docs/ROADMAP.md) for what's next and the open questions.
