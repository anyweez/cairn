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
