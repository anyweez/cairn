# Roadmap and open questions

## Built in the first slice

- `cairn`: Decider, Decision, Refusal.
- `codec`: versioned sum-type encoding with upcasters.
- `ledger`: streams with sorted pessimistic locks, an append-only entry log,
  snapshots, an outbox, projections over an xid8-ordered log, idempotency
  keys, and replay verification.
- `spec` and `spectest`: rules, laws, invariants, refinement, framework rules
  D1–D7, rule health, and `Fails` for mutant tests.
- `blocks/fsm`: lifecycle tables, with a law and a Mermaid rendering.
- `examples/decisions`: lore's register, spec-checked and running on Postgres.
  Its spec includes one proposed rule (DR8, the acknowledgement gap) that the
  current behaviour violates.

## Built since

- `cmd/cairn-manifest`: the spec manifest. Rules, stored types with their
  JSON shapes, and fsm tables, committed as `spec.manifest.yaml` and checked
  in CI. Pull requests also fail when a stored type changes in a way that
  breaks reading entries already in the ledger.

## Next

1. **Semantic spec diff.** Run base and head specs over the same corpus
   (seeded runs plus ledger exports) and report examples tightened or
   loosened, model changes, implementation violations, production messages
   that would now be refused, and rules that became vacuous.
2. **Linearizability.** Use porcupine over concurrent `Handle` calls, with
   the sequential Decider as the model. The concurrent supersede test in the
   example is a hand-written instance of this check.
3. **Lint.** Add analyzers for:
   - core purity: a package containing a Decider imports no clock, randomness
     or I/O;
   - model independence: a model doesn't import the implementation;
   - rule hygiene: unique IDs, and no rule left proposed for too long.
4. **`blocks/bag`.** A counted multiset with caps and a slot or weight
   budget, the shape of the LegendsReborn inventory, with its rules built in.
5. **Ledger export.** Dump a stream or kind as JSONL, so it can serve as a
   spec corpus or reproduce a production bug locally.
6. **LISTEN/NOTIFY.** Wake projections and dispatchers on commit instead of
   polling.

## Open questions

- **Redaction.** Entries are append-only, enforced by a trigger. Honouring a
  deletion request needs a sanctioned path, either tombstoning an entry's
  payloads or encrypting per subject and discarding the key. Which one should
  the library offer, and should events be allowed to carry personal data at
  all?
- **Refusals advance the stream sequence.** Recording them is the point (they
  are the evidence for "this change would refuse N real requests"). A refused
  message to a stream that doesn't exist also creates that stream. But a
  client spamming refused messages grows the stream. Rate-limit in the shell,
  or store refusals in a separate table?
- **Effect ordering.** The outbox guarantees at-least-once delivery but not
  ordering. Is per-stream ordering worth the cost of a per-stream cursor?
