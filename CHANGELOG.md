## v0.1.0 (2026-10-14)

### Feat

- **spec**: Change, a law over the state alone, and Kind checked against the mechanism
- **ledger**: Aggregate.Validate, run before every method
- **ledger**: entry views, a read model written once per entry
- **ledger**: one migration for the v0.1.0 schema
- **ledger**: version results through a codec
- **ledger**: projections queue their writes in a Batch
- **ledger**: views queue their writes in a Batch
- **ledger**: record who sent each message, through what, and why
- **ledger**: views, read models written in the message's transaction
- **cairn-manifest**: write the spec manifest as YAML
- spec manifest of rules, stored types and fsm tables
- **ledger**: locking a stream no longer creates it
- **spectest**: fail Check when an established rule is vacuous
- scaffold cairn — spec-checked deciders on a Postgres ledger

### Fix

- **ledger**: pass JSON parameters as text, so simple protocol works
- **ledger**: rebuild entry views in windows, and pass them no stored position
- **ledger**: Migrate leaves a newer schema alone, so a rollback starts
- **ledger**: address review of the state cache stack
- **ledger**: Load reads a damaged stream on its own connection
- **ledger**: keep one snapshot per state version
- **cairn-manifest**: close compatibility-check gaps found in review
- **ledger**: order the global log by transaction ID as a number
- keep the global log in stream order and fence outbox claims

### Refactor

- **ledger**: Divergence.Aspect is a typed constant
- **ledger**: Tx runs statements, and nothing more; Apply says how it reports a refusal
- **ledger**: Projection runs like Dispatcher, and both share one poll loop
- **ledger**: Handle returns no state, and Aggregate.Clone is gone
- **ledger**: Transact takes the streams to lock; Tx.Lock is gone
- **ledger**: Entry, Delivery and Divergence name their stream as a Stream
- **codec**: one codec.Set for the ledger and the checker
- **codec**: remove Codec.Types and codec.Type

### Perf

- **spectest**: skip shrinking in Fails
- **ledger**: trust a stream's cached state by how often it's right
- **ledger**: the state cache is on by default, and safe to be
- **ledger**: a state cache that records in one round trip
- **ledger**: Handle in two round trips
