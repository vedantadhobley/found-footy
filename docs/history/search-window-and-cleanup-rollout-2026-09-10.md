# Search-window and cleanup rollout — 2026-09-10

## Release and scope

The user approved pushing four `feat/event-update` commits and deploying
`e344cf65c3b6e8a53c511d7440bd0ff0932d89cd`. The remote branch was verified at
that exact commit before deployment. The normal pre-push `make check` gate
passed, including the real-Postgres suite (232.622s); no hook was skipped.

The release includes:

- FF-091: an immutable first-observation search cutoff, guarded known-video
  stopping, durable recovery, and the browser's applied-window acknowledgement.
- FF-073: browser ownership from debounce/checklists, immutable removal targets,
  aggregated failures, and versioned staging-cleanup retries.
- Offline quality/overlap experiments and the previous release documentation.
  These experiments do not change production clip-quality selection.

Search query strings, aliases, vision acceptance, API/NATS contracts, schema,
and production configuration are unchanged. There was no migration, frontend
rollout, historical replay, live search probe, or manual data repair.

## Execution

`make deploy-prod` ran from the clean development checkout at the exact commit.
Its ignored dotenv input matched the prior release checkout's input. No new
release checkout was needed; no Compose or dotenv file was edited.

The unchanged release script used sequential legacy-builder Compose builds
with invocation-only 4 GiB build caps. Its non-root permission smoke was capped
at 128 MiB and passed. Build identity: `2026-09-10T05:08:20Z`.

All four MLS matches were full-time, with no running discovery workflows or
event browsers. The script rechecked browsers after building and recreated only
API, both workers, and static Twitter at 05:11:25–26 UTC (01:11 Eastern).
It printed `release verified` for the exact SHA. VNC stayed stopped.

Postgres, Temporal Postgres, Temporal, Temporal UI, and Garage retained their
container IDs and original startup times. No old image, detached production
volume, or persistent profile was deleted.

## Verification

- All four processes exposed the expected SHA, image tag, and build timestamp;
  all had zero restarts. Worker/API health returned success.
- Both workers verified the database migration ledger/schema and found their
  four existing schedules. Startup checks found no WARN/ERROR records.
- Static Twitter reported `healthy`, `verified`, and idle. Authentication and
  cookie backup succeeded at 05:11:28 UTC. Its only mount is `/config`.
- The exact search image passed the isolated storage lifecycle smoke, using
  synthetic cookies: legacy-mount removal, restart versus replacement, and
  persistent cookie/named-profile storage. All test resources were removed.
- REST returned all four finished fixtures with complete event phases. The
  corresponding discovery checklists were closed. Their poll timestamps
  advanced to 05:13 UTC after the rollout.

The image has no implicit volumes, but Docker omitted the absent `Volumes`
map. The smoke's direct field template therefore required an invocation-only
compatibility substitution:

```text
{{with index .Config "Volumes"}}{{len .}}{{else}}0{{end}}
```

This preserved the zero-volume assertion and all lifecycle checks; no test was
skipped. The checked-in smoke should adopt that absent-map handling as a small
tooling follow-up, recorded under FF-090.

## Remaining acceptance

FF-091 and FF-073 remain validating. Observe a natural new event's fixed cutoff,
early-stop permission transitions, and recovery without starting synthetic
production work. Reaper eligibility and failed-release retries still need
natural production evidence. The tests cover these boundaries but do not prove
complete X recall or reduced request cost.

One globally open discovery row predates this match day: the August 15
`discovery-smoke` checklist for K. Davis. It has no running Temporal workflow or
browser and was not repaired during this rollout.

Current ownership and next actions remain in the [issue register](../todo.md).
