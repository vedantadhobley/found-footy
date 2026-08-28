# Required transition audit commits with state

## Context

Monitor previously wrote fixture/event state through a repository and then
called a standalone Composer to append `event_log`. Every Composer error was
discarded. Postgres could therefore retain a real transition without the row
described as its durable forensic evidence.

Making the second call return an error would not close the gap: a retry after
the first commit could duplicate or misorder evidence, and a process exit
between calls would still lose it.

## Decision

Activation, completion, known-event detection, the first stable debounce
crossing, and debounce-zero removal commit their typed `event_log` record in
the same Postgres transaction as the authoritative mutation.

`internal/contract/auditlog` owns the semantic kinds and payloads. The concrete
fixture/event repositories own persistence. Monitor depends on narrow audited
repository extensions only where this transaction is required. Ordinary poll
refreshes and provider-mutable field updates remain unaudited CRUD operations.

The standalone Composer and its dedicated metrics are removed. Live NATS
publication remains an independent invalidation plane; it is not part of the
database transaction.

## Consequences

- An audit insert failure rolls the state mutation back and the Temporal
  activity retries the complete unit.
- Idempotent debounce workflow votes emit `event.stable` and `event.removed`
  only on the transition they perform.
- Postgres query tracing observes audit inserts; there is no separate
  `found_footy_event_composer_*` metric family.
- The current durable transition vocabulary has five kinds.

## Superseded contract

This preserves the archived decision that `event_log` is the audit plane while
superseding its [standalone Composer write boundary](./archive-through-2026-08-16.md#2026-08-14--composer-decoupled-to-event_log-only-n2n8-kindlogtype-rename-deferred).
