# Event-browser names follow workspace order while labels authorize lifecycle

## Context

FF-001 made raw Docker event browsers environment-scoped, but composed the
daemon name as `ff-firefox-<network>-ev-<uuid>`. That was unique but inverted
the workspace's canonical `<project>-<env>-<role>` order. The first version of
the production release gate then coupled itself to the older unscoped name
prefix, proving that name-pattern authorization was fragile.

The [original FF-001 decision](./archive-through-2026-08-16.md#2026-08-16--firefox-fleet-ownership-follows-the-compose-selected-network-ff-001)
remains authoritative for Compose-selected ownership, labels, and Temporal
network-alias compatibility. This decision supersedes only its daemon-name
shape and makes the release selector independent of that shape.

## Decision

A dynamic event browser uses
`<compose-network>-firefox-ev-<full-event-uuid>` as its daemon-global name. The
current Compose networks produce `found-footy-prod-firefox-ev-<uuid>` and
`found-footy-dev-firefox-ev-<uuid>`, consistent with the workspace
`<project>-<env>-<role>` convention.

The name deterministically locates an event container. It does not authorize a
mutation. Start, release, and reap operations must also verify the fleet,
event, and scope labels plus attachment to the configured network. Fleet-wide
release guards select by fleet label and target-network membership instead of
parsing names.

The existing `ff-firefox-ev-<event-prefix>` network alias remains unchanged so
open and replayed Temporal histories resolve the same address.

## Consequences

- Dev and prod names remain daemon-global and collision-free.
- Operators can identify the owning project, environment, role, and event from
  the name.
- A foreign container cannot gain deletion authority by copying the name.
- Release automation survives future display-name changes as long as the fleet
  ownership contract remains stable.
