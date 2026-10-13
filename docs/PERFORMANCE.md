# Performance of the ledger

What `Aggregate.Handle` costs next to code that updates a row in place, where
that cost goes, and which of it has to stay on the path a caller waits on.

## Measurements

A throwaway benchmark (not committed) ran against a local Postgres 16 on a
4-vCPU VM, over one connection. The machine and disk were noisy, so read the
ratios, not the absolute numbers. The decider is the README's bank account
with one event per message and no effects.

Each `Handle` against short streams, next to two traditional baselines:

| Path | Statements | fsync on | fsync off |
|---|---|---|---|
| `UPDATE … SET balance = balance + $1` (autocommit) | 1 | 0.74–1.07 ms | 0.15 ms |
| `BEGIN; SELECT … FOR UPDATE; UPDATE; COMMIT` | 4 | 1.0–2.1 ms | 0.37 ms |
| cairn `Handle` | 9 | 2.4 ms | 1.23 ms |

"fsync off" is `synchronous_commit = off`. Removing the commit flush leaves
cairn's own overhead visible.

- When the commit flush dominates, `Handle` costs about 1.2–2.4× a
  read-then-write transaction. Both pay for one commit flush.
- Without the flush, `Handle` costs about 3.3× that transaction and 8× a
  single `UPDATE`. That's 9 sequential round trips plus JSON encoding and
  decoding. Over a network the gap widens: at 0.5 ms a round trip, 9 trips
  take about 4.5 ms against about 2 ms for the transaction.

One more `Handle` on a stream that already holds N entries:

| N | No snapshots | Snapshots (every 100) |
|---|---|---|
| 10 | 2.8 ms | 3.8 ms |
| 1,000 | 10.5 ms | 3.0 ms |
| 5,000 | 58.5 ms | 2.6 ms |

Snapshots are off unless `StateVersion` is set. Without them, every `Handle`
reads and folds the whole stream (`load` in `ledger/aggregate.go`), so its cost
grows with the stream's history. Refusals are entries too, so refused messages
also lengthen what is read on every later `Handle`. With snapshots on, the
cost stays flat.

## The critical path today

For one stream, `Handle` makes these round trips in order, all while holding
the stream's advisory lock:

1. `BEGIN`
2. Check that no transaction ID is assigned yet (`Tx.Lock`)
3. Hash the stream names into lock keys (`Tx.Lock`)
4. `pg_advisory_xact_lock`, once per stream (`Tx.Lock`)
5. Read `streams.seq` (`Tx.Lock`)
6. Read the snapshot, when snapshots are on (`load`)
7. Read the stream's entries since the snapshot (`load`)
8. Look up the idempotency key, when one is given (`duplicate`)
9. Upsert the `streams` row (`Tx.append`)
10. Insert the entry (`Tx.append`)
11. Insert one outbox row per effect (`Tx.append`)
12. Write a snapshot, every `SnapshotEvery` entries (`maybeSnapshot`)
13. `COMMIT`

The lock is held through the commit flush. One stream can therefore take at
most 1 / (transaction latency) messages per second: about 400/s in the run
above, and fewer over a network. Different streams don't contend, apart from
sharing the database.

Already off the path: effect delivery (`Dispatcher`), projections, and
`Verify`.

## What has to stay on the path

- **The commit.** A returned `Handled` means the decision, its events and its
  effects are durable and committed together. That requires the caller to
  wait for one commit flush, which a traditional system pays as well.
- **The stream lock and the append.** The next decision on a stream depends on
  this one's state.

## Recommendations

Ordered by payoff.

1. **Turn snapshots on by default,** or reject or warn about an empty
   `StateVersion`. Folding the whole stream is the only cost that grows with
   history, and it grows without bound.
2. **Keep a state cache in memory,** keyed by stream and holding
   `(seq, state)`. `Tx.Lock` already reads the authoritative `seq` under the
   lock. When the cached seq matches, skip trips 6–7 and all event decoding;
   when it doesn't, load as today. That's safe across processes because the
   seq check happens under the lock. It saves two round trips and all
   decoding on hot streams.
3. **Merge round trips.**
   - Trips 2–5 can become one statement or one `pgx.Batch`. Locks must still
     be taken in key order.
   - Trips 9–11 can become one statement (a CTE that upserts the stream,
     inserts the entry and inserts the outbox rows) or one batch.
   - That takes `Handle` from 9 or more trips to about 4. It matters most
     when the database is across a network.
4. **For hot streams, commit several queued messages for the same stream in
   one transaction.** Each caller still waits for that commit, so no
   guarantee weakens, and throughput is no longer capped at one flush per
   message.
5. **Decide whether refusals need a durable write.** Today a refused message
   costs a write transaction and a flush, where a traditional system would
   only read. One option is `SET LOCAL synchronous_commit = off` for
   transactions that record only refusals.
   - Cost: a crash can lose the refusal record, and an idempotent retry would
     then be decided again instead of returning the stored refusal.
   - What stays safe: no state can be lost, since refusals carry no events.

   This is a product decision, not an obvious win.

## Storage

Each entry is a row of JSON with five indexes on `cairn.entries`, and the log
is append-only. Write-ahead-log volume and storage are therefore several times
what an update-in-place design produces, and they grow without bound. This
costs disk and replication bandwidth more than latency.

## Caveats

- One connection, one machine, a virtual disk. Contention across many streams
  under concurrency wasn't measured; it should scale like ordinary row locks,
  since streams don't share locks.
- Statement counts were taken with a pgx query tracer, so `BEGIN` and `COMMIT`
  are included.
