# Running-stoppage rollout — September 18, 2026 UTC

## Release

Production runs `06524ffbd17adbf0c0378dd6d7c0e4a2f882db23` from
approximately 02:35:10 UTC, with build identity `2026-09-18T02:32:19Z`.
[FF-097](../todo.md#ff-097--running-stoppage-clocks-inherit-the-next-period)
ships `vision-evaluate-v4`: a positive allowance beside a running clock
inside the documented post-boundary windows retains the ending period.
The measured minute, tolerance, explicit labels and other validation gates
remain unchanged. See the [decision](../decisions/2026-09-18-running-stoppage-retains-ending-period.md).

No schema migration, frontend/NATS contract, Twitter query, model selection,
prompt or historical-candidate repair changed. FF-095/096 extraction work
and the unrelated dirty workspace were excluded. Provider policy remains
shadow-only.

## Gates and verification

- Isolated branch `release/ff097-clock-v4`, checkout
  `/tmp/found-footy-ff097-release.cWnbJnSx`. The environment link uses
  unchanged project configuration. No production Compose, environment,
  Caddy or stateful data file was edited.
- Normal pre-commit hooks passed. Full `make check` passed: formatting,
  module tidiness, vet, pinned lint (zero issues), unit/integration/scenario
  tests. PostgreSQL integration completed in 367.271 seconds. Scoped race
  tests and saved-evidence replays passed before packaging.
- No EventWorkflow or scoped event browser was running before release.
  The deployment script rechecked the browser fleet after building.
- The standard `make deploy-prod` used invocation-only 4-GiB tool/build
  limits and sequential builds. The isolated storage and non-root
  permission smokes passed. Only synthetic smoke containers/storage were removed.
- The command exited zero with `release verified` for the exact commit.
  Both workers, API and static Twitter report that SHA. They returned healthy
  responses and had zero restarts; selected structured startup logs contained
  no WARN/ERROR/FATAL records.
- Postgres, Temporal Postgres, Temporal, Temporal UI and Garage retained
  their exact IDs and start times. VNC remained stopped.
- Gueye's targeted REST event remains complete with no videos. Deployment
  does not replay old rejections or rewrite historical evidence.

## Separate model blocker at V4 release

This application release was healthy at the process/API level, **not an
end-to-end vision validation**. Production still pins `gemma-4-12b`, which
Control no longer serves. A bounded request returned HTTP 404
`model_not_found`. Startup currently probes catalog reachability but does
not check that an explicit model pin appears in that catalog; green process
health therefore does not establish model availability.

The requested replacement trial used the catalog ID `gemma-4-e4b`.
[Ten test calls](../design/audits/gemma-e4b-vision-trial-2026-09-18.md) completed
but invented period/clock evidence. It was not promoted. Benchmark the available
31B vision model and settle the production pin before running recovery.
Do not clear the pin and rely on catalog ordering to choose a vision model.

Control later restored `gemma-4-12b`, completed its admission-fairness rollout,
and passed the frozen runtime workload. Found Footy's
[V5 rollout](./vision-v5-rollout-2026-09-18.md) then qualified and deployed the
literal-period contract at exact temperature zero. That later evidence resolved
this release-time blocker without changing the model ID, application deadline,
or application concurrency contract.

## Recovery result

The first proposed batch is only Gueye's four known FF-097 candidates:

| Candidate | Retained source |
|---|---|
| `82d11826-e163-4b07-879c-fbb84de9c062` | TheGoalsZone / 2100681627579879691 |
| `b5d0a8a3-8241-4efc-9606-e854d4b5a937` | LigActu / 2100682895727714725 |
| `65755e8d-6b8d-437e-ab1d-2ec2a64dcbf6` | DAZN_FR / 2100683277136707729 |
| `43bfea20-59f8-4ed2-8a04-6bfbce00dfdb` | LigasTopFutbol / 2100683528685900248 |

A read-only transaction first confirmed that all four remained rejected for
the clock mismatch, their retained frames carried `+4` with no period label,
the event was not removed, and its original discovery completed September 17
at 20:33:37 UTC. These were four source posts, not four established unique
clips.

Release `280fddc` added a fail-closed candidate allowlist to the historical
runner. The selector is canonicalized into the downstream checklist, must match
on retry, and remains compatible with older fixture-wide replay records. Its
production dry run matched exactly this event and these four rows before the
separately approved apply.

Workflow
`event-replay-ff097-running-stoppage-5956a72d-75cc-4f7e-bf54-bdf1afc83cd7`
(run `3b71391a-fbd3-4225-83fe-8fd87b81a91c`) completed in approximately 15
seconds with `assets_surfaced`: one promoted source, one exact duplicate, one
source whose post no longer exposed a video variant, and one CDN download
failure after retries. It performed no fresh Twitter search and did not
force-promote a candidate.

The promoted asset `9bb95389-1b9d-5286-9e3a-567c5924593b` is 1280×720 at 60
fps with direct support 2. Active share `s_d25fdaa3148f` is rank 1 and retains
an extracted minute of 46. Its immutable `vision-evaluate-v5` evidence reads
`46:35`, `46:37`, and `46:39`, each beside `+4`; all three frames normalize to
first-half stoppage time and verify the expected 45+2 event. The workflow
published `event.update` after placement and again after checklist completion.
The checklist completed as `assets_surfaced` with the exact four candidate IDs
preserved in its replay metadata.

The V4 release's previous application image was `a2a6830`; all releases in this
record were schema-compatible. Any rollback, production model configuration
edit, or additional historical repair requires its own approval. Neither this
release nor its documentation was pushed remotely.
