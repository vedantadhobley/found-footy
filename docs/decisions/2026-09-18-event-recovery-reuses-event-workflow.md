# Event recovery reuses EventWorkflow under a new identity

## Context

A successfully completed EventWorkflow owns durable search counters and
candidate terminal state. Starting its deterministic `event-<event-id>` identity
again either receives Temporal's already-started result or restores fifteen
completed attempts. The existing dev trigger lacks production guards. The
historical clock-reject runner safely reopens a narrow candidate selector, but
intentionally performs no new Twitter search.

The September 18 Kane 54′ discovery completed with no public clips while the
shared model path was saturated. Some candidates have typed transient failures,
and later Twitter results may also exist. Recovery must preserve the original
execution and attach any new clips to the existing event.

## Decision

Ship an inert `found-footy-ops` binary beside the normal application binary.
Its dry-run-first `replay-event` command registers a new deterministic
checklist/Temporal identity and then starts the existing EventWorkflow with the
stored event input. It provisions the same event-scoped Firefox address that
natural discovery uses. No admin HTTP endpoint or second discovery workflow is
introduced.

The operation slug is mandatory and part of the durable identity. Preparation
locks the event and permits only a completed fixture, a named non-removed event,
a completed original discovery, zero pending candidates and no other pending
discovery owner. Registration commits before Docker or Temporal effects, so a
process failure is recoverable by repeating the same command.

Three modes separate intent. Fresh search resets only the new checklist's
attempt counter. Transient retry checkpoints search at the schema ceiling so
configuration drift cannot reopen it, and reopens only the fixed v1
typed-failure allowlist: model admission/request failures, Temporal validation-
activity timeouts and CDN failures. Combined recovery does both. Reopened
candidates retain their previous terminal evidence under
`outcome_detail.replay`; existing URLs remain search exclusions.

## Consequences

Every recovered clip passes through the normal download, hashing, vision,
selection, ranking, publication and cleanup contracts. Existing assets remain
visible during recovery, and the public event identity does not change.

The allowlist is deliberately conservative. Deterministic evaluator rejects,
untyped historical failures, filesystem/input failures and candidates already
attached to assets require separate reviewed repair policies. Expanding this
selector changes recovery semantics and must land with evidence and tests.

Dry-run planning is read-only. Applying a production replay performs Postgres,
Docker and Temporal mutations and still requires explicit per-operation user
approval.

## Production validation

Release `d256c7e` ran operation `kane-54-20260918` against the completed Kane
54′ event. Preparation selected 19 typed transient failures. The normal
EventWorkflow completed 15 usable searches with no unavailable observations,
closed with zero pending candidates, and released its scoped Firefox instance.
The fresh searches added 48 candidate URLs, but no candidate produced a public
asset; every candidate ended in a deterministic rejection or download failure.
