# Storage and diagnostics rollout — 2026-09-09

## Release and scope

The user approved pushing the four pending `feat/event-update` commits and
deploying `a7c9f539171eb8f8d644ea5cb33e23b6cb0b2ba7`. GitHub
`origin/feat/event-update` now points to that commit. The normal pre-push
`make check` gate passed, including the real-Postgres integration suite.

The release contains:

- FF-090: disposable headless Firefox profiles and anonymous-volume removal
  when releasing owned fleet containers.
- FF-087: durable vision-failure causes and separate local-admission/request
  timing. This adds diagnosis, not a saturation fix.
- FF-075: the stable empty-postponed-scoreboard classification repair.
  The semantic circuit evaluator remains non-enforcing.
- The September production-audit documentation.

Uncommitted quality, cadence, overlap, and picture-quality experiments stayed
in the development worktree and were excluded from the release. Search queries,
aliases, quality selection, API/NATS contracts, and the database schema did not
change. No migration or frontend deployment was required.

## Execution

`make deploy-prod` ran from a clean detached checkout of the exact commit.
The temporary release checkout is
`/tmp/found-footy-release-a7c9f53.hgmdEr`; it reuses the existing ignored `.env`
through a symlink. This is a release artifact, not a new workspace convention.
No application bind mount depends on that temporary checkout. Infrastructure
bind mounts retain their existing development-checkout paths.

The unchanged release script built with the legacy builder, sequential Compose
builds, and an invocation-only 4 GiB build-memory cap. The permission-smoke
container was capped at 128 MiB. No Compose or environment file was edited.
Build identity: `2026-09-09T21:51:09Z`.

The quiet-window checks found no running discovery workflows or event browsers.
The release script checked event browsers again after building. It recreated
only API, both workers, and static Twitter at approximately 21:59:05 UTC
(17:59 Eastern), then printed the exact `release verified` success marker.
VNC remained stopped. Postgres, Garage, Temporal, Temporal Postgres, and Temporal
UI retained their container IDs and original startup times.

## Verified behavior

- All four application processes exposed the expected SHA, image tag, and build
  timestamp, with zero restarts after startup.
- Worker and API health returned success. REST returned a valid fixture/event
  snapshot with the existing presentation and phase fields.
- Both workers verified the migration ledger/schema, registered with Temporal,
  and found all four existing schedules. No worker startup errors were emitted.
- Static Twitter authenticated at 21:59:09 UTC and successfully refreshed its
  cookie backup. Its only mount is the persistent `/config` bind; no anonymous
  `/data` volume remains attached. The exact image declares zero volumes.
- The exact headless release image passed the isolated built-image storage
  smoke: legacy-mount cutover, restart versus replacement, cookie-bind
  persistence, and named-login-profile persistence. The smoke used synthetic
  cookies and removed only its own temporary containers and volumes.
- Active polls completed at 21:59:30 and 22:00 UTC; staging also completed at
  22:00 UTC. The latter active poll fetched both tracked fixtures without errors
  and returned a trusted provider-integrity aggregate with zero regressions.
  Cincinnati–DC remained postponed; this release does not implement FF-088's
  deferred-fixture retirement policy.

## Remaining acceptance and boundaries

The next tracked kickoff was 23:30 UTC (19:30 Eastern). No synthetic goal or
workflow replay was started for this rollout. FF-090 remains validating until
a natural event browser confirms the new mount shape and normal removal.
FF-087 still needs a natural exhausted vision failure with durable cause detail.

No fixture/event/candidate repair or Garage mutation was performed. Existing
detached anonymous volumes were not reclaimed; exact-target attribution and
separate deletion approval remain required. The earlier approved legacy
named-volume deletion is recorded in the [cleanup evidence](./storage-cleanup-2026-09-09.md).

Current issue ownership remains in the [issue register](../todo.md).
