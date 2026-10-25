# Performance of the ledger

What `Aggregate.Handle` costs, what limits throughput, and how to size a
deployment for it. cairn is meant to run against a database across a network,
so costs are counted in round trips: over a link of a few milliseconds, they
are nearly all of what a message costs. [BENCHMARKS.md](BENCHMARKS.md) has the
measurements and how to reproduce them.

## What each call costs

| Call | Round trips |
|---|---|
| `Handle`, on a stream in the state cache | 1, shared by the messages recorded together |
| `Handle`, on a stream not in it (its first message here, evicted, stale, or the cache off) | 2 |
| `Handle` with an idempotency key that matches, not cached | 2 |
| `Handle` with an idempotency key that matches, cached | 4 |
| `Handle` on a stream another process wrote since this one did | 4, or 2 once the cache has seen it's shared |
| `Load` | 1 |
| `Transact` with `LoadLocked` and `Apply` on one stream | 5 |
| `CatchUp`, per batch of up to `BatchSize` entries | 5 |

`TestEachPathKeepsToItsRoundTrips` holds the uncached paths to these numbers,
and `cache_test.go` the cached ones.

Without the cache, `Handle` makes two round trips:

1. `BEGIN`; lock the stream and read its seq (`cairn.lock_streams`); read its
   snapshot; read the entries after the snapshot; look up the idempotency key.
2. Upsert the stream; insert the entry; insert its effects into the outbox;
   write a snapshot when one is due; write the views; `COMMIT`.

Between the two, the decider runs in your process. Deciding needs the state
from the first trip, and recording needs the decision, so with the decider in
Go and a lock taken before reading, two is the floor.

The stream's lock is granted partway through the first trip and held until
the commit, about 1.5 round trips plus the commit's flush. Without the cache,
**that sets how many messages one stream can take:**

> messages per second on one stream ≈ 1 ÷ (1.5 × RTT + commit flush + decide time)

That's about 160/s at a 3 ms round trip, and about 110/s at 6 ms. Different
streams don't share a lock, so total throughput is limited by connections
instead.

## The state cache

A store keeps the state of up to 10,000 streams in memory by default.
`ledger.StateCacheSize(n)` sets the size, and `StateCacheSize(0)` turns the
cache off. That bounds the number of streams, so its cost in memory is the
count times a state's size: a few hundred bytes a state is a few megabytes at
most, and a megabyte a state is gigabytes. `ledger.StateCacheBytes(n)` adds a
bound in bytes, estimated from the snapshots the store already writes, and
evicts the least recently used streams to stay under it. It is off by
default, because how much memory a deployment can spare is its own choice.
The estimate is the size of a stream's last snapshot, so it trails a state
that has grown since, and an aggregate with no `StateVersion` writes none to
size; those keep the count as their only bound. Each stream that an aggregate
writes has a lane in the process, and its messages are recorded one
transaction at a time:

- **A cached stream costs one round trip.** Its message is decided as soon as
  it is its turn. One batch then opens a transaction, locks the stream on
  condition that it is still at the cached seq (`cairn.lock_stream_at`), and
  writes the entry, effects, snapshot and views, and commits it. The lock is
  held for no round trip at all, only the commit's flush.
- **Messages that arrive together are recorded together.** Those that queue
  while a stream's transaction is in flight are decided in order and recorded
  in its next transaction, up to 100 at a time. Each caller still returns
  only once its own message is committed. Views and snapshots are written
  once per transaction, from the state after its last message.
- **A stale cache records nothing.** If another process has written the
  stream, the lock fails, as does a duplicate idempotency key on its unique
  index. Nothing is recorded, and the messages are decided again from the
  stored state, in two round trips. The failed attempt costs two of its own:
  the batch that raises, and the rollback of the aborted transaction it left
  behind, so the first stale `Handle` costs four. A failure that spoils a
  shared transaction is narrowed down by halving the group: a half the
  database accepts is recorded whole, and only a failing half is halved
  again, so one bad message costs a few transactions rather than one per
  message in the group. Only failures the database reported are retried.
  When it is unknown whether a commit happened, the callers get the error;
  when a batch's transaction is known to have committed, the error wraps
  `ErrRecorded`.
- **A stream other processes write too is read before each decision.** Every
  message shows whether the cached state would have been right. One decided
  from the cache either succeeds or goes stale; one read first finds the
  stream at the cached seq or past it. The cache keeps a moving average of
  that for each stream, and decides from the cached state only while it was
  right at least three times in four. Otherwise it reads first, in two round
  trips, which is what no cache would cost. A stream that becomes one
  process's alone is trusted again within about seven messages. Hot streams
  still get their messages recorded together in each process.

At a 3 ms round trip, this took one caller on a hot stream from 99 to 170
messages a second. Eight callers went from 154 to 654, about four messages
to a transaction. Thirty-two streams on 32 connections went from 2,863 to
5,322 ([BENCHMARKS.md](BENCHMARKS.md)).

What it relies on:

- **Each stream's traffic mostly reaching one process.** That's where the
  cache pays. Without it, the cache falls back to about what no cache costs.
- **Pure deciders.** `Decide`, `Evolve` and views are handed the cached state,
  so they must not change it. spectest holds deciders to this. Nothing hands
  it to a caller: `Handled` carries no state, and `Load` reads the stream.
- **Views that only care about the latest state.** A view sees only the state
  after its transaction's last message. A view that fails for one message's
  state therefore fails that message only when it is the last in its
  transaction.

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
- **Waiting for a lock holds a connection.** Without a cache, a caller queued
  behind a hot stream's lock keeps its connection idle until the lock is
  granted. Many callers on one hot stream can therefore take a whole pool and
  stall unrelated streams. With a cache, callers in one process wait in its
  lane instead, and the stream uses one connection there. Bound how long
  callers wait, with context deadlines or `lock_timeout`, and keep hot
  streams' traffic on few instances.
- **A transaction-mode pooler works.** cairn keeps no session state: its
  locks last only for the transaction, and it sets nothing on the session
  and listens for nothing. So it runs behind PgBouncer, or Supabase's pooler,
  in transaction mode. pgx's default exec mode prepares named statements,
  which such a pooler can send to a server connection that never prepared
  them unless it tracks prepared statements itself (PgBouncer's
  `max_prepared_statements`). Otherwise set
  `default_query_exec_mode=cache_describe` in the connection string, or
  `simple_protocol`. Under `cache_describe` the first send of each statement
  on a connection costs one extra round trip, to describe it; later sends
  don't. CI runs the ledger's tests through PgBouncer in transaction mode
  (`scripts/test-pgbouncer.sh`).
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

1. **Fewer round trips for the dispatcher.** It makes about two per effect
   (restarting the lease and counting the attempt, then recording the outcome).
   That's off the path a caller waits on, but it limits how fast effects are
   carried out over a slow link.
2. **Decide whether refusals need a durable write.** A refused message costs a
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
- **The outbox is a queue, not a log.** A dispatcher deletes an effect's row
  as it records the delivery, so the table holds only work outstanding or
  failed; a failed row stays with its error for an operator to look at. Without
  that, every effect ever produced would stay in the table and in its indexes.

## Running several instances

Every guarantee holds across processes, because all coordination happens in
Postgres:

- **Locks:** advisory locks per stream, re-read under the lock.
- **Seq:** a unique `(stream, seq)` constraint.
- **Effects:** outbox leases fenced on attempts.
- **Projections:** compare-and-set checkpoints.

An entry's `recorded_at` comes from the clock of the process that recorded
it, taken once per transaction, so that views can be told it before anything
reaches the database. Processes' clocks differ, so `recorded_at` can disagree
with the order of the log. The log is ordered by transaction ID and position,
never by time.

During a rolling deploy:

- **Schema:** `Migrate` leaves alone a schema that a later release has
  already migrated, so old code keeps starting during the rollout and after
  a rollback. Migrations only add; none changes what an earlier release reads.
- **Snapshots:** they are kept per `StateVersion`, so old and new code don't
  evict each other's. A release that changes what `Evolve` computes must
  change `StateVersion` too: otherwise new code reuses old code's snapshot as
  its base, and a stream that has one keeps the old fold while a stream
  without one rebuilds from events.
- **New versions:** a new message or event version that new code writes
  can't be read by old code until the rollout finishes. Ship decoding first,
  and start writing the new version in a later release.
