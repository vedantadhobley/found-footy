# Event updates use a coordinated quiet-window cutover

## Context

The [event-update decision](./2026-09-04-event-updates-own-async-projection.md)
initially called for a temporary dual-subject consumer. Vedanta selected a
coordinated hard cutover instead. Found Footy producer `dbc2a76` and shared
schemas `fcfb28f` are committed. The frontend agent reports consumer `ca1f8e5`
implemented and tested against those commits, but not pushed or deployed.

## Decision

Use only `found-footy.<env>.event.update` in the new consumer. Do not add a
temporary `event.video` listener. Producer and consumer must roll out within
one verified quiet window; mismatched versions are not compatible.

Before rollout, confirm zero running EventWorkflow executions, no named events
debouncing, and sufficient time before the next kickoff. Recheck immediately
before the approved production action; a prior quiet-window check is not a
reservation. If discovery resumes, defer this cutover until it settles.

Deploy the matching consumer and producer releases within that window. Refresh
already-open browser tabs onto the new JavaScript bundle and obtain a full REST
snapshot. SSE reconnect alone cannot upgrade a browser's loaded code. Verify
release identities and the NATS-to-SSE-to-client path before declaring success.

Retain `PublishEventVideo` as a Temporal activity for old workflow histories.
It forwards to the new wire subject; this is not dual wire compatibility. No
database migration is required by this change.

## Consequences

This supersedes only the original rollout sequence. Event upserts, targeted
parent recovery, snapshot recovery, and the fixture/event ownership boundary
remain unchanged. Natural no-candidate completion must still prove that the
browser changes from `searching` to `complete` without a refresh. Commit, push,
and production rollout status must be recorded separately; this decision does
not authorize production changes.
