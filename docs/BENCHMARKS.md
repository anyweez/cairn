# Benchmarks

`BenchmarkHandle` (`ledger/bench_test.go`) measures how many messages per
second `Aggregate.Handle` records when every round trip to Postgres is slow,
as it is when the database is across a network.

## Running it

```sh
eval "$(scripts/bench-postgres.sh)"
go test ./ledger -run '^$' -bench Handle -benchtime 5s -count 3
```

`scripts/bench-postgres.sh` starts the test Postgres and a
[toxiproxy](https://github.com/Shopify/toxiproxy) in front of it. The
benchmark migrates each database directly, then records messages through the
proxy, which delays every response from Postgres by the chosen latency.
`CAIRN_BENCH_RTT_MS` picks the latencies (default `0,2,5`). Compare runs with
`benchstat`.

Each message is shaped like an ordinary aggregate's: snapshots on, an
idempotency key, one effect and one view. Each case starts from a fresh
database and warms up every connection before timing.

| Workload | Callers | Connections | Streams | What it shows |
|---|---|---|---|---|
| `hot/callers=1` | 1 | 1 | 1 | One message's full latency |
| `hot/callers=8` | 8 | 8 | 1 | The most one stream can take while callers queue on its lock |
| `spread/callers=32/pool=32` | 32 | 32 | 32 | Throughput with no contention, one connection per caller |
| `spread/callers=32/pool=4` | 32 | 4 | 32 | The same callers on pgxpool's default pool size for a 4-CPU host |

Besides `ns/op`, each case reports:

- `msgs/s`, and latency at the median and the 99th percentile;
- `trips/op` and `stmts/op`, counted with a pgx tracer. `BEGIN` and `COMMIT`
  count, and a batch counts as one trip however many statements it carries;
- `rtt-ms`, the median of 21 `SELECT 1` queries through the proxy before
  timing. **Read results against this, not the configured latency.** On
  Docker Desktop, adding 2 ms gives a measured round trip of about 3.2 ms,
  and adding 5 ms gives about 6.2 ms.

## Baseline

Run on 2026-10-13: an Apple M5 Pro (15 cores), Postgres 17 in Docker Desktop,
on a branch where views queue their writes in a `Batch`. Each figure is the
median of three 5-second runs. Throughput is in messages per second; latency
is the median.

| Added latency | Measured RTT | `hot/callers=1` | `hot/callers=8` | `spread`, pool 32 | `spread`, pool 4 |
|---|---|---|---|---|---|
| 0 ms | 0.1 ms | 464/s (2.1 ms) | 466/s | 2,167/s | 808/s |
| 2 ms | 3.2 ms | 18/s (54 ms) | 24/s | 503/s | 68/s |
| 5 ms | 6.2 ms | 11/s (93 ms) | 14/s | 302/s | 41/s |

Every run made 13.0 round trips per message:

1. `BEGIN`
2. Check that no transaction ID is assigned yet
3. Hash the stream names into lock keys
4. Take the advisory lock
5. Read `streams.seq`
6. Read the snapshot
7. Read the entries since the snapshot
8. Look up the idempotency key
9. Upsert the stream
10. Insert the entry
11. Insert the effect into the outbox
12. Send the view batch
13. `COMMIT`

What the numbers show:

- **Round trips set the latency.** With added latency, one message takes
  about 13 round trips plus a few milliseconds: 54 ms at 3.2 ms, and 93 ms
  at 6.2 ms.
- **A hot stream barely gains from more callers.** Eight callers on one stream
  get only about 1.3 times what one caller gets. The lock is held from step 4
  through the commit, which is about 9.5 of the 13 round trips, so callers
  mostly wait in line.
- **The pool size caps total throughput.** Thirty-two callers on 4 connections
  record a seventh to an eighth of what they do on 32 connections. That is
  about 4 connections ÷ 54 ms per message.
- **Tail latency is noisy.** Some `spread` runs show a p99 of 170–300 ms when
  their median is 60–100 ms, and other runs of the same case don't. The cause
  isn't known yet. Candidates are snapshot writes (every 100th message on a
  stream) and the Docker VM's disk. Compare medians across changes until
  that's explained.

## Handle in two round trips

The same benchmark, machine and method, after `Handle` was rewritten to send
its statements in two batches. The first begins the transaction, locks the
stream and reads its snapshot, entries and idempotency key. The second
records the entry, its effect, any snapshot and the view, and commits. Every
run made 2.0 round trips per message.

| Added latency | Measured RTT | `hot/callers=1` | `hot/callers=8` | `spread`, pool 32 | `spread`, pool 4 |
|---|---|---|---|---|---|
| 0 ms | 0.1 ms | 879/s (1.0 ms) | 868/s | 5,477/s | 1,991/s |
| 2 ms | 3.0 ms | 100/s (9.2 ms) | 161/s | 3,036/s | 423/s |
| 5 ms | 6.2 ms | 59/s (16 ms) | 109/s | 2,005/s | 249/s |

Next to the baseline:

| Measured RTT | `hot/callers=1` | `hot/callers=8` | `spread`, pool 32 | `spread`, pool 4 |
|---|---|---|---|---|
| 0.1 ms | 1.9× | 1.9× | 2.5× | 2.5× |
| 3 ms | 5.5× | 6.7× | 6.0× | 6.2× |
| 6.2 ms | 5.4× | 7.8× | 6.6× | 6.1× |

- **The slower the link, the bigger the gain:** 2.5× or less on a local
  connection, 5–8× at 3–6 ms.
- **A hot stream now gains from more callers,** 1.6–1.8× instead of 1.3×. The
  lock is held for about 1.5 of the 2 round trips, and none of that time
  depends on how much a message writes.
- **The pool still caps total throughput,** at about connections ÷ latency:
  4 ÷ 9.2 ms ≈ 435/s, against 423/s measured. Pool size remains the setting
  that matters most for many streams.
- **Tails are tighter.** No case's p99 was more than about twice its median
  except one `pool=4` run on the local connection.

## With a state cache

A store's state cache is on by default. The plain runs turn it off with
`StateCacheSize(0)`, and the `/cache` runs leave it on. Both were measured
from the same build, on 2026-10-13. Medians of three 5-second runs.

| Measured RTT | Workload | 2 round trips | Cache | Gain | Round trips per message |
|---|---|---|---|---|---|
| 3 ms | `hot/callers=1` | 99/s (9.3 ms) | 170/s (6.0 ms) | 1.7× | 1.0 |
| 3 ms | `hot/callers=8` | 154/s | 654/s | 4.3× | 0.25 |
| 3 ms | `spread`, pool 32 | 2,863/s | 5,322/s | 1.9× | 1.0 |
| 3 ms | `spread`, pool 4 | 422/s | 753/s | 1.8× | 1.0 |
| 6 ms | `hot/callers=1` | 61/s (16 ms) | 105/s (9.7 ms) | 1.7× | 1.0 |
| 6 ms | `hot/callers=8` | 110/s | 427/s | 3.9× | 0.25 |
| 6 ms | `spread`, pool 32 | 2,014/s | 3,701/s | 1.8× | 1.0 |
| 6 ms | `spread`, pool 4 | 258/s | 469/s | 1.8× | 1.0 |

- **A cached stream costs one round trip,** so latency and the connections
  each message holds both roughly halve.
- **A hot stream gains most.** Eight callers on one stream are recorded about
  four messages to a transaction (0.25 round trips per message). That's 4×
  without the cache, and over 25× the 13-round-trip baseline.
- **On the local connection, `spread` with pool 4 is too noisy to compare.**
  Both paths swing between about 1,300/s and 3,200/s from run to run, with
  p99s of 130–210 ms in the slow runs. The Docker VM's disk stalling under
  that many commits is the likely cause. Every other local case improved
  with the cache: 2,194/s against 907/s for the hot stream with 8 callers,
  and 7,840/s against 5,601/s for 32 streams on 32 connections.

### One stream written by two processes

`hot/callers=2/instances=2` splits one stream's callers between two stores,
each with its own cache, as two processes behind a load balancer that doesn't
route by stream would be. Both write at once, so neither process's cache
stays current.

| Measured RTT | No cache | Cache | Round trips per message, cached |
|---|---|---|---|
| 0.1 ms | 934/s | 911/s | 2.0 |
| 3 ms | 175/s | 167/s | 2.0 |
| 6 ms | 116/s | 113/s | 2.0 |

- **The cache costs about what no cache does here,** within 3–5% at the
  median, about the run-to-run noise. Once it has seen that a stream's cached
  state is right less than three times in four, it reads before deciding,
  which is the uncached path.
- **The first version did much worse.** It went back to trusting the cache
  after a single read found the stream where the cache had it. With two
  writers racing, that happens by chance about half the time. It made 2.47
  round trips a message and recorded 97/s at 3 ms, because each failed
  attempt also waited behind the other process for the lock.
  `TestAStreamTwoProcessesWriteAtOnceCostsNoMoreThanWithoutACache` fails
  against that version.
