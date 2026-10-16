# Passive Twitter accounting rollout — 2026-10-16

## Scope and release

The user approved a telemetry-only production rollout. Release
`62af131f7bd292f00cd1cb792746d560a0382e9d` replaced both workers, the API and
static Twitter at `16:35:04Z`. All four reported the exact release identity.
The [accounting decision](../decisions/2026-10-16-twitter-admission-starts-with-passive-accounting.md)
defines the bounded observations and unchanged search behavior.

No admission controller, request routing, new wait, concurrency restriction,
query change, retry change, migration, replay, frontend release or production
configuration edit was included. The separate uncommitted FF-089 geographic
CDN-denial classifier remains undeployed. The release contains only passive
accounting plus its tests, research evidence and design documentation.

## Source isolation and validation

The primary checkout also contained the separate CDN-denial patch. Only
accounting source and matching documentation hunks were committed; the
remaining local edits were preserved. Deployment used a clean detached
temporary checkout of `62af131`, linked to the unchanged existing `.env`.
No new permanent checkout layout or deployment convention was introduced.

The normal pre-commit hook passed. Exact-checkout format, module, vet and lint
gates passed. Its first unit run exposed an existing tooling limitation:
Docker mounted the worktree at `/src`, but its `.git` file pointed outside
that mount. The tooling-scope test could not resolve Git metadata. This
occurred before any production build/recreation.

The test rerun supplied a read-only mount of that Git metadata through the
existing `DOCKER_VOLS` override; both modules' complete short suites passed.
Source and configuration stayed unchanged. No hook or test was skipped.
The standard `make deploy-prod` command then built the immutable images,
passed the non-root scratch/socket smoke, rechecked the clean source and empty
event-browser fleet, recreated the four application processes, and verified
their identities. Prior focused accounting race checks passed.

## Production acceptance

- Preflight found no running Temporal workflows or scoped event browsers.
  The single unfinished SQL checklist belongs to the known August 15
  `discovery-smoke-f975f431-6d4d-471f-adc6-5607116f3432`; it was not changed.
- The first upcoming fixture was Çorum FK–Rizespor at `17:00Z`, after rollout.
- Before and after rollout, loaded environment values matched the canonical
  Compose model and existing `.env`; only expected release-image tags changed.
  Comparisons printed key names only, never secret values.
- Both workers and the API returned health HTTP 200 with zero restarts.
  Static Twitter returned health HTTP 200, `healthy`, and not busy.
- Both workers logged successful startup of `found-footy` and
  `found-footy-vision`. Active polling advanced through `16:38:00Z`.
- The public-read API listener returned HTTP 200. A targeted read of fixture
  `1584432` retained its `upcoming` projection and empty event array.
- Retained share `s_1a6c80af981c` returned HTTP 302. The signed redirect was
  neither followed nor printed; no full media or model call was issued.
- PostgreSQL, Garage, Temporal, Temporal PostgreSQL and Temporal UI retained
  their container IDs, start times and restart counts. No stateful service was
  recreated. The optional VNC image was built by the standard release command,
  but no VNC container was started.
- At acceptance, no recent candidate remained pending and no discovery was
  running. No additional authenticated X search was issued for validation.

## Natural evidence still pending

The next normal searches will emit `twitter_timeline_measured` and propagate
optional `evidence.timeline` into client/workflow/history evidence. Correlate
copies by `scan_id`; do not sum browser and worker copies. Inspect actual
request cost, quota headers, failures after a rendered feed, unfinished or
unmatched events, detail truncation and first-candidate latency.

This release does not prevent Twitter 429s or recover CDN-denied clips.
Scarce-request scheduling and enforcement remain proposed FF-038 work, subject
to the measured coverage/latency gates. No natural request summary had arrived
during acceptance; startup success is not natural-search validation.
