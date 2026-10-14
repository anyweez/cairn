# Redaction and personal data

The ledger is append-only, so it can never be asked to forget. That is the
guarantee `Verify` and replay stand on, and it is not negotiable: the trigger
refuses every update and delete. A deletion request is answered outside the
ledger, and the ledger is arranged so there is nothing in it to delete.

## The rule

**Messages, events, effects, results and entry metadata carry no personal
data.** They carry a decision: a subject named by an opaque identifier, and
what was allowed, refused or changed. Names, addresses, tokens, secrets,
comment text and any other content live in the application's own mutable
tables, keyed by the same identifier, where ordinary deletion works.

This is the line the README already draws inside a subsystem — the issue's
lifecycle belongs in cairn, its comment text does not. Redaction makes it a
rule rather than a preference, because anything in an entry is permanent.

The rule covers metadata too. `Actor`, `Via`, `CorrelationID` and `Extra`
are part of the entry, so an actor is a pseudonymous id, never an email
address or a display name.

## Erasure

Honouring a deletion request is the shell's job. Keep the personal data in a
table keyed by the subject, and on request delete those rows. The decision
history in the ledger keeps referring to the subject by its pseudonymous id
and stays verifiable, because it never held the data that was erased.

When an application must be able to link a person to their stream, that link
is a per-subject key the application holds outside the ledger. Erasing the
person means destroying the key; the ledger keeps only the pseudonym it
always had. This is per-subject encryption with key discard, applied to the
identity mapping rather than to the log — the only place it leaves the log's
guarantees intact.

## What the library does not offer

**Tombstoning a payload is rejected.** Rewriting an entry's message, events
or result breaks the three things the ledger is for. `Load` rebuilds state by
folding stored events, so a redacted event makes the stream unreadable.
`Verify` re-decides stored messages, so a redacted message becomes a
permanent divergence rather than a clean history. And a tombstoned row is not
erasure: the same values sit in snapshots, the outbox, the views an
application wrote, and every replica, backup and export, none of which the
library owns. A tombstone trades a guarantee away for a claim it cannot keep.

**The library does not encrypt entry payloads for you.** Encrypting payloads
per subject has the same replay problem once a key is discarded, because the
events are what state is rebuilt from; and key management and I/O do not
belong on the record path, which must stay out of `Decide` and `Evolve` and
within `Handle`'s round-trip budget. An application that genuinely must record
personal data can encrypt it before it becomes a message and accept that
discarding the key ends replay for that stream. The library makes no promise
about entries that do that.

## A deletion request, in practice

1. The application keeps a row per subject in its own schema, holding the
   personal data, keyed by the opaque id the ledger's stream uses.
2. A message in the ledger may name that id; it may not name the person.
3. On a deletion request, delete the row and destroy any key that maps the
   person to the id.
4. `Verify` and `Load` are unaffected: they only ever saw the id.
