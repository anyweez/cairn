# Performance of the ledger

What `Aggregate.Handle` costs, what limits throughput, and how to size a
deployment for it. cairn is meant to run against a database across a network,
so costs are counted in round trips: over a link of a few milliseconds, they
are nearly all of what a message costs. [BENCHMARKS.md](BENCHMARKS.md) has the
measurements and how to reproduce them.

## What each call costs

| Call | Round trips |
|---|---|
| `Handle` | 2 |
| `Handle` with an idempotency key that matches | 2 |
| `Load` | 1 |
| `Transact` with `Lock`, `LoadLocked` and `Apply` on one stream | 5 |
| `CatchUp`, per batch of up to `BatchSize` entries | 5 |

`TestEachPathKeepsToItsRoundTrips` holds `Handle`, `Load` and `Transact` to
these numbers.

`Handle`'s two round trips:

1. `BEGIN`; lock the stream and read its seq (`cairn.lock_streams`); read its
   snapshot; read the entries after the snapshot; look up the idempotency key.
2. Upsert the stream; insert the entry; insert its effects into the outbox;
   write a snapshot when one is due; write the views; `COMMIT`.

Between the two, the decider runs in your process. Deciding needs the state
from the first trip, and recording needs the decision, so with the decider in
Go and a lock taken before reading, two is the floor.

The stream's lock is granted partway through the first trip and held until
the commit, about 1.5 round trips plus the commit's flush. **That sets how
many messages one stream can take:**

> messages per second on one stream ≈ 1 ÷ (1.5 × RTT + commit flush + decide time)

That's about 160/s at a 3 ms round trip, and about 110/s at 6 ms. Different
streams don't share a lock, so total throughput is limited by connections
instead.

## Sizing the pool

Each `Handle` holds a connection for its whole transaction. By Little's law,
the connections a workload keeps busy are:

> connections ≈ messages per second × `Handle` latency

`Handle` latency is about 2 round trips, plus the commit's flush, plus a few
milliseconds. It was 9 ms at a 3 ms round trip and 16 ms at 6 ms. So 1,000
messages per second at a 3 ms round trip needs about 9–10 connections busy
on average. Allow headroom for bursts.

- **Set the pool size explicitly.** pgxpool's default is the larger of 4 and
  the number of CPUs, and it caps throughput at about 4 ÷ latency, or 423/s
  at a 3 ms round trip in the benchmark. Set `pool_max_conns` in the
  connection string, or `MaxConns` on `pgxpool.Config`.
- **Count connections across instances.** Instances × `MaxConns` must stay
  under Postgres's `max_connections`, with room for everything else that
  connects.
- **Waiting for a lock holds a connection.** A caller queued behind a hot
  stream's lock keeps its connection idle until the lock is granted. Many
  callers on one hot stream can therefore take a whole pool and stall
  unrelated streams. Bound how long callers wait, with context deadlines or
  `lock_timeout`, and keep hot streams' traffic on few instances.
- **Read from replicas only where staleness is fine.** Advisory locks exist
  only on the primary, so every `Handle` goes there. `Load` against a replica
  works but can return stale state.

## What has to stay on the path

- **The commit.** A returned `Handled` means the decision, its events, its
  effects and its views are durable and committed together. The caller waits
  for one commit flush, as it would in a traditional system.
- **The stream lock and the append.** Each decision on a stream depends on the
  one before it.

Already off the path: effect delivery (`Dispatcher`), projections and
`Verify`.

## What would raise throughput next

Ordered by payoff.

1. **Cache state in memory, and decide before locking.**
   - Keep `(seq, state)` per stream after each commit. When a message
     arrives for a cached stream, decide it in Go right away. Then send one
     batch: `BEGIN`, lock, a compare-and-set that fails unless the stream is
     still at the cached seq, the writes, `COMMIT`. If the seq doesn't
     match, run the two-trip path instead.
   - That's one round trip, and no network time at all while the lock is
     held: about the commit's flush.
   - It is safe across processes because the compare-and-set runs under the
     lock. It needs the decider to be pure, which cairn already requires.
   - It pays off for streams whose traffic reaches one process, so route by
     stream ID.
2. **Commit several messages on one hot stream together.**
   - Queue the messages that arrive for a stream while its commit is in
     flight. Decide them in order, then record them in one transaction.
   - Every caller still waits for that commit, so no guarantee weakens, and
     one stream is no longer limited to one commit at a time.
   - Views write a stream's whole state, so a batch of messages needs only
     one view write.
3. **Fewer round trips for the dispatcher.** It makes about two per effect
   (restarting the lease, then recording the outcome). That's off the path a
   caller waits on, but it limits how fast effects are carried out over a
   slow link.
4. **Decide whether refusals need a durable write.** A refused message costs a
   write transaction and a flush. `SET LOCAL synchronous_commit = off` for
   transactions that record only refusals would save the flush, but not a
   round trip, so over a network it is worth little. If a crash loses a
   refusal, an idempotent retry is decided again instead of being answered
   from the stored refusal. No state can be lost, because refusals carry no
   events. This is a product decision more than a performance one.

## Storage

- **Every entry is a row of JSON, and the log is append-only.**
  `cairn.entries` has seven indexes:
  - its primary key;
  - `(stream, seq)`;
  - idempotency keys;
  - the global log, and the global log by kind;
  - actors;
  - correlation IDs.
- **Writes cost more than updating rows in place.** Write-ahead-log volume and
  storage are several times what an update-in-place design produces, and they
  grow without bound.
- **This costs disk and replication bandwidth more than latency.** Each
  `Handle` also updates its `cairn.streams` row. A lower `fillfactor` on
  `cairn.streams` and `cairn.state_snapshots` lets those updates stay on the
  same page, which writes less.

## Running several instances

Every guarantee holds across processes, because all coordination happens in
Postgres:

- **Locks:** advisory locks per stream, re-read under the lock.
- **Seq:** a unique `(stream, seq)` constraint.
- **Effects:** outbox leases fenced on attempts.
- **Projections:** compare-and-set checkpoints.

During a rolling deploy:

- **Snapshots:** they are kept per `StateVersion`, so old and new code don't
  evict each other's.
- **New versions:** a new message or event version that new code writes
  can't be read by old code until the rollout finishes. Ship decoding first,
  and start writing the new version in a later release.
