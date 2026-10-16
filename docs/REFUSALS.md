# Refusals and unbounded growth

Recording a refusal is the point. It is the evidence for "this rule would
refuse N real requests", `Verify` replays it with today's code, and an
idempotent retry is answered from the entry that first answered it. That
record is not free: a refused message is an entry, it advances the stream's
sequence, and a refusal to a stream that doesn't exist yet creates that
stream. A caller free to send without limit can therefore grow a stream, and
a ledger, without limit.

## The rule

**Every refusal the ledger can store is an entry in its stream. The library
does not rate-limit refusals and does not keep them in a separate table.
Bounding abusive traffic is the application's job, at the request edge,
before the message reaches `Handle`.**

A quota per actor, per subject or per stream belongs in the shell that owns
the request — a gateway, a handler — not in the ledger. That is the layer
that can tell an abusive caller from a busy one: cairn sees a stream and a
message, never an identity or a request rate. Require `Meta.Actor`, key the
quota on the actor or the subject, and refuse at the edge when it is
exceeded.

## Why not a rate limit in the shell

Whether a refusal is recorded would depend on a per-stream counter and the
moment the message arrived. The same messages, replayed against the same
code, would be recorded differently on a faster instance or a later day,
and `Verify` could no longer re-decide exactly what was stored. The log is
ordered by transaction ID, never by time, and its value is that it does not
depend on when or where it was written; a clock in the record path takes
that away. Deciding whether to record is a decision, and decisions do not
see time. A limit also caps the *rate* a stream grows, not its size: a
patient caller still grows it without limit.

## Why not a separate table

Keeping refusals outside `cairn.entries` moves the growth rather than
bounding it. The same spam writes the same number of rows, now somewhere
else, and a refusal that does not advance the stream's sequence still needs
a key and an order of its own — a second sequence to keep in step with the
first.

It also splits the record. `Verify` walks a stream's entries in sequence and
re-decides each one, refusals included; `IdempotencyKey` answers a retry
from the entry that first answered it; `ReadAll` and projections read one
ordered log. Refusals in a side table would have to be merged back into all
of those.

The stream a first refusal creates is part of the same point. It is the
evidence that a subject's very first request was refused, which is exactly
what a rollout gate wants to count; not creating it would drop those
requests from the record.

## What an application does

1. Authenticate the caller, and put a quota in front of the decider — per
   actor or per subject.
2. Refuse at the edge when the quota is exceeded, before `Handle`, so the
   abusive traffic never reaches the ledger.
3. Where a request may be retried, pass an `IdempotencyKey` so the retry is
   answered from its first entry instead of recorded again.
4. Read a spike of refusals as the signal it is: the count is the evidence,
   and keeping it is what makes it visible.

## What the library does not offer

**It does not decide which refusals are worth keeping.** A refusal is the
same as an accepted message: one entry, one sequence number, one commit. The
ledger cannot tell a spammer from a client, because nothing in the entry says
which is which, and it will not guess with a clock or a counter.
