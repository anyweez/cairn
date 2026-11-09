# Effects and delivery order

A decision records events, which are its own history, and effects, which are
work for the outside world: send an email, charge a card, tell an index. The
ledger stores an effect in the same transaction as the entry that decided it,
and a `Dispatcher` carries it out afterwards. That delivery is at least once;
order is not part of it.

## The rule

**The outbox delivers every effect at least once, in no guaranteed order.
The library does not order deliveries per stream and keeps no per-stream
cursor. An effect handler must be safe to run more than once and must not
depend on the order deliveries arrive in.**

At-least-once is what the outbox is for: an effect is not lost, even when the
process carrying it out dies. Ordering would be a second guarantee, and this
is the decision not to make it.

## What is guaranteed

- **In the entry's transaction.** The effect is queued with the entry, so if
  the entry is stored its effects are, and a rolled-back transaction leaves
  neither.
- **At least once, possibly more.** A dispatcher claims an effect with a
  lease and retries it on failure. A delivery can repeat after a crash, a
  lease that expired, or an outcome that could not be recorded.
- **Not in order, within a stream or across streams.** A later entry's effect
  can be claimed while an earlier one is being retried, and a retried effect
  can arrive after a newer one. The claim takes the lowest `id` that is due,
  not the lowest position in each stream.
- **Several dispatchers, one effect at a time.** Claims use `SKIP LOCKED`, so
  dispatchers sharing a kind take different effects; each is handed to one
  `Handle` at a time.

`Delivery.Key()` identifies an effect as `stream#seq.index`, and
`Delivery.Seq` and `Delivery.Index` locate it in its stream. Use them to
recognise a repeat.

## What a handler must do

An effect handler must be **idempotent** and **order-insensitive**. Two
patterns cover most effects, and are often used together:

- **Key the outward call on `Delivery.Key()`.** Pass the key to the system the
  effect acts on, so a repeat is recognised there: an idempotency key in a
  payment API, an upsert keyed by the effect's identity.
- **Make the effect carry the state, not a change to it, and drop an older
  delivery.** An effect that names the state the stream is now in can be
  applied again, or late, without undoing a newer one. Keeping the `Seq` the
  state came from lets the handler ignore a delivery older than what it holds.
  The reference index in `examples/decisions` does this.

## Why not per-stream ordering

Ordering a stream's effects would need state the outbox does not keep: a
cursor per stream naming the next effect to deliver. It would have to advance
only when the effect before it is done, be fenced against a lease that
expired and was reclaimed, and be given back when a dispatcher stops with part
of a batch undelivered. That is a second set of delivery state with the
failure modes the claim's attempt count already solves.

The cost it buys is worse than the state. **A failing effect would block every
later effect of its stream.** `MaxAttempts` defaults to zero, retrying
forever, so a poison effect or a broken downstream would stall that stream
until an operator marked the effect failed. Giving up on the effect instead is
a policy the library cannot choose: only the application knows whether a
payment that cannot be charged should block a shipment, be skipped, or be
compensated.

And the guarantee would be partial. A cursor orders one stream's effects and
nothing across streams, so an application whose effects interact across
streams still needs its own order downstream.

An opt-in claim guard — deliver an effect only when no earlier effect of its
stream is pending — is smaller than a cursor, since it needs no new state. It
has the same head-of-line cost, and it is not worth adding before an
application asks for a guarantee it can accept.

An application that needs its effects applied in order therefore keeps that
cursor over the deliveries it receives, where it can decide what a stuck
effect means.
