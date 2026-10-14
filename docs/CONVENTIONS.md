# API conventions

Rules for cairn's public API. Each comes with the reason for it, so a case the
rule doesn't foresee can be decided from the reason.

## Database work goes through a batch

**A public API that lets callers write to the database inside a cairn
transaction takes a `*ledger.Batch` to queue statements on. It never hands
out a `pgx.Tx`, a connection or a pool to run them.**

```go
// Do this: the caller queues, cairn sends.
Write func(b *ledger.Batch, at ledger.Loaded[S], cause ledger.Entry) error

// Not this: every statement the caller runs is its own round trip.
Write func(ctx context.Context, tx pgx.Tx, at ledger.Loaded[S], cause ledger.Entry) error
```

### Why

cairn is meant to run against a database across a network, where a round trip
costs far more than anything else `Handle` does. The cost is highest while a
stream is locked, because the lock is held across every round trip until the
commit. Each round trip there both slows the caller and lowers how many
messages one stream can take per second.

cairn can only cut its round trips if it decides when statements are sent.
With a `Batch`, every statement a caller queues goes out with cairn's own
writes, so all of them together cost one round trip, and later work can merge
that trip with others. A callback that holds a transaction runs each statement
the moment it's called. That fixes the transaction's shape for as long as the
API exists.

### What it asks of callers

- **Pass in everything a callback needs, so it has nothing to read.** A view
  gets the stream's whole state and the entry that caused the write, not just
  a key to look them up with.
- **Write set-based SQL instead of reading and then writing.** Use upserts
  (`INSERT … ON CONFLICT`), `INSERT … SELECT`, and statements that replace a
  stream's rows from its whole state.
- **Errors still abort.** A callback that returns an error, or a queued
  statement that fails, aborts the transaction. The error names the owner and
  which of its statements failed, even though everything was sent together.

### Exceptions

An API may hand out a transaction only when its operation can't be batched:
it needs a result from the database to decide what to write next, and that
decision can't be moved into the SQL. No current API qualifies.
An exception must say on the API itself why it can't batch and that each
statement costs a round trip, and it must be kept off the path a caller waits
on wherever possible.

### Where the API stands

| API | Status |
|---|---|
| `View.Write` | Takes a `Batch`. Views' statements go out with the message's own writes and its commit. |
| `EntryView.Write` | Takes a `Batch`. Each entry's statements go out with the message's own writes and its commit, after the views. |
| `Projection.Apply` | Takes a `Batch`. A batch of entries' statements goes out in one round trip, together with the checkpoint's advance. It also takes a `ctx`, for read models that live outside the database. |
| `Store.Transact` / `Tx` | `Tx` is a `pgx` transaction, for transactions that span several streams. Callers compose cairn's own calls in it, and each costs one round trip: `Transact` locks every stream at once, then `LoadLocked` per stream, and `Apply` per message (plus one to look up an idempotency key). `Transact` adds `BEGIN` and `COMMIT`. |

cairn's own statements follow the same rule. `Handle` costs one round trip
when the store's state cache holds the stream, and two otherwise: one begins
the transaction, locks the stream and reads it, and the other records the
decision, its effects, any snapshot and the views, and commits. `Load` costs
one. `TestEachPathKeepsToItsRoundTrips` holds the uncached paths to their
budgets and `cache_test.go` the cached ones; `docs/PERFORMANCE.md` has the
full table.
