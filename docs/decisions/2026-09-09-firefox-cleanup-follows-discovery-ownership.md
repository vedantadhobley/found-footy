# Firefox cleanup follows discovery ownership

## Context

FF-073 exposed three gaps in the [original reaper](./archive-through-2026-08-16.md):
individual Docker removal failures were discarded; the staging workflow allowed
only one sweep attempt and skipped cleanup when vendor polling failed; and an
active fixture kept browsers even after discovery completed. FF-090 fixed the
storage removed with a container, not these ownership and failure boundaries.

Regression tests also reproduced a stale-name race: a listed container could
disappear and be replaced under the same event name before deletion. Resolving
that name again could remove the replacement using the old container's age.

## Decision

Use the existing event and downstream-checklist rows, not a new lease table:

- Removed events never own browsers.
- Before any discovery checklist is registered, a positive debounce counter
  on an active fixture owns warmup. This includes the crash/retry gap between
  the trigger vote and checklist registration; requiring only `triggered=false`
  would create an unsafe ownership gap.
- Any pending downstream checklist continues to protect the browser regardless
  of fixture state. This preserves late discovery and pending replay work.
- Completed discovery does not retain a browser solely because its fixture
  remains active. Anonymous zero-counter placeholders do not own warmup.

Every activity attempt reloads ownership and container inventory. An enabled
fleet without a usable ownership repository fails before deletion. Keep the
existing 120-second minimum container age and 15-minute staging cadence.

Cleanup lists immutable Docker container IDs, rechecks each ID's event/scope
labels and network, then stops/removes that ID. It never substitutes a newer
container with the same name. Container-not-found is successful absence, even
when another cleanup removes it after inspection. Other removal errors remain
failures. Anonymous-volume deletion stays enabled; cookie binds and named
operator profiles remain persistent.

A sweep continues through independent failures while its context is alive and
returns joined errors plus partial successful names. This lets activity failure
reach Temporal instead of becoming false success. Failed Temporal attempts do
not persist a partial output; retries relist actual state and skip deleted IDs.

New staging histories attempt cleanup even after vendor polling fails. The
cleanup activity has at most three attempts, a 60-second per-attempt limit, and
a three-minute total limit including queue/retry time. Exhaustion remains in
the staging result's errors; the next scheduled cycle is another recovery path.
The `ff-073-fleet-reaper` version marker preserves old histories' single attempt
and early return after a failed vendor poll.

## Boundaries and verification

No schema, event identity, search, authentication, public API/NATS, or Compose
change is required. Browser cleanup stays in the existing workflow and Docker
adapter; no new service, scheduler, pre-provisioning, or daemon-wide prune exists.

This is not a Temporal liveness detector or an atomic database/Docker lease
service. Pending checklists remain conservative ownership, including stale rows;
their recovery belongs to FF-025/FF-007. The change does not reclaim previously
detached anonymous volumes or authorize production repair/deletion.

Tests cover partial failure and retry, concurrent not-found, replaced containers,
changed ownership, cancellation, minimum age, disabled/missing ownership paths,
real-Postgres lifecycle boundaries, bounded Temporal retries, vendor failure,
and old-history compatibility. Production rollout needs separate approval and
natural lifecycle verification. Current status lives in the
[issue register](../todo.md#ff-073--firefox-reaper-acknowledges-failed-releases).
