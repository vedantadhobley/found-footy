# Maitland-Niles restoration and revalidation preview — 2026-09-13

Subsequent execution: the separately approved [production repair](../../history/maitland-niles-restoration-2026-09-13.md)
committed at 22:32 UTC, with public 31/6 and verified SSE delivery. The preview
below preserves the evidence and authorization boundary before that write.

## Result and scope

For Everton–Manchester United fixture `1557390`, Maitland-Niles 90+6′ event
`01dedc54-da16-4e3c-9539-ac43c81ffd77`, the deployed planner would restore
the seven-second original and return public scores **31, 6** when existing
share acceptance is preserved and the four never-public variants receive their
fresh accepted evaluations. Two other selected singletons stay filtered.

This is a read-only preview, not an applied repair. Fresh evaluations are new
evidence, not reconstructed September 6 model responses. A separate attempt to
replace every old classification with today's result was stopped because one
previously accepted variant received a false clock rejection. No result was
retried to obtain a preferred verdict.

The user approved keeper inspection, fresh validation and selection preview.
Production SQL, Garage objects, shares, scores, candidate attribution, workflow
state and notifications were not changed. No deployment or application-code
change occurred. Related work: [FF-081](../../todo.md#ff-081--pairwise-quality-policy-is-not-a-stable-cluster-order),
[FF-093](../../todo.md#ff-093--accepted-vision-evidence-expires-with-workflow-history),
and the clock boundary recorded under [FF-094](../../todo.md#ff-094--added-time-announcement-incorrectly-unpins-a-running-clock).

## Method and evidence

One read-only SQL statement at 21:57:31 UTC exported the event's ten assets,
six shares, 34 accepted-source identities and zero retained own evaluations.
All ten originals were fetched successfully, totaling 42,052,506 bytes;
file sizes and MD5s matched their rows. The three files from the earlier
[visual review](../../history/direct-support-rollout-2026-09-13.md#maitland-niles-visual-review)
were reused. Object keys, not share redirects, selected the exact bytes.

The offline harness imports the unchanged activity, ffmpeg adapter, LLM client
and pure domain planner from deployed commit `8c836e0`. No diff exists in these
packages against that commit. It substitutes local read-only media for Garage
and has no database, Temporal or NATS client. Disposable containers have explicit
memory/CPU caps. The decoder and planner have no network; validation makes one
gateway request at a time. Worker settings and `/v1/models` were checked first.

Validation used the production three-image request: probed duration at
25/50/75%, JPEG quality 3, default prompt/schema, `reasoning_effort: none`,
model `gemma-4-12b`, API 90+6′ and tolerance ±1. Ten requests completed between
22:01 and 22:03:49 UTC without transport errors or retries. One took 41.9 s;
the other nine took 9.8–11.5 s. Local files retain exact sampled JPEGs, raw model
responses and evaluated outputs. Accepted outputs carry actual evaluation IDs,
timestamps, model identity and contract digests, with no invented workflow origin.

| Asset prefix | Exact sightings | Prior own share | Fresh result |
|---|---:|---|---|
| `7131e1d0` | 15 | Unverified, superseded | Unverified: period interpretation |
| `d0ef0afe` | 1 | None | Unverified: period interpretation |
| `01ff6353` | 7 | Unverified, superseded | Rejected: invented secondary timer adds five minutes |
| `b609a521` | 2 | None | Unverified: period interpretation |
| `32b50be4` | 1 | Unverified, superseded | Unverified: period interpretation |
| `f5274482` | 1 | Unverified, active | Unverified: period interpretation |
| `3227d1d5` | 4 | None | Unverified: period interpretation |
| `8c19fa8b` | 1 | Unverified, active | Unverified: no clock |
| `358b5db8` | 1 | None | Unverified: period interpretation |
| `191fc025` | 1 | Unverified, active | Unverified: no clock |

## Current keeper and direct overlap

The current keeper `f5274482-6ef0-5a5f-b84f-d07b7eefa79c`, share
`s_52350ba4e42f`, is 47.58 seconds at reported 1280×720 / 29.97 fps.
Three-second contact-sheet samples show the same strike as the original,
followed largely by celebrations; score changes 1–2 to 2–2. It has a different
scorebug/broadcaster overlay. The seven-second original includes the strike,
not a different goal or only celebrations. Restoring it supplies a focused cut,
not proven independent footage or a newly established picture-quality winner.

Their retained direct hashes fail both deployed routes:

| Route | Strongest aligned span, including misses | Required span |
|---|---:|---:|
| Hamming ≤12, at most 3 misses | 28 frames | 30 frames |
| Hamming ≤16, at most 5 misses | 31 frames | 50 frames |

Failure to match is not proof of different content. No crop experiment or
threshold change was made here. The two bridge variants still directly match
both clips and supply four plus one source observations to each. The original
also has one exact observation; its direct support is therefore six, not eleven
or the sum of other keepers' cached scores.

## Three preview modes

The actual `PlanSelection` uses retained hashes, present-byte checks, the
unchanged 12/27-of-30 and 16/45-of-50 routes, and the existing selected roots.
This is additive restoration after recorded placement, not a replay of the
quality tournament with a new ordering. Public projection applies unchanged
FF-078 filtering and the production share comparator.

| Evidence mode | Proposed public clips | Disposition |
|---|---|---|
| Historical own shares only | Current keeper 23; restored original remains hidden at 1 | Demonstrates missing-evidence loss; not recommended as a repair |
| Preserve shares; fill four never-public variants with fresh acceptance | Current keeper 31, original 6 | Supported bounded repair preview |
| Replace all classifications with fresh results | No plan emitted | Rejection of `01ff6353` needs explicit treatment, not silent removal |

The bounded plan reuses original share `s_19a9f2fb7ea2`, clears that asset's
supersession, routes its one observed source back to itself, and resolves the
other hidden assets directly to the current keeper. It retains all 34 source
identities. Selected scores are 31/6/1/1: their sum of 39 includes five shared
support memberships and is not a claim of 39 distinct tweets. Existing public
rank 1 remains rank 1; the restored original becomes rank 2. Both remain
unverified. The two singleton shares remain durable and filtered.

The historical-only regression reproduces the prior independent 23/1/1/1
calculation and checks that planning does not mutate the snapshot. A second
regression verifies the real fresh-evidence preview returns 31/6 and restores
only the expected original. These are local audit tests, not new app behavior.

## Fresh clock failure — not a historical rejection claim

Asset `01ff6353-48b1-52b5-a0d7-cc7df19d3b84` returned these real observations;
all three had `soccer=true`, `screen=false`, `period=null`, `added=null`:

| Main clock | Model's secondary clock | Normalized minute |
|---|---|---:|
| `95:04` | `05:00` | 100 |
| `95:15` | `05:00` | 100 |
| `95:27` | `05:00` | 100 |

The first actual model input visibly contains a running `95:04` timer and a
separate `+5` announcement, not a `05:00` secondary timer. The model misassigned
the field despite the prompt's distinction. `parseFrameClock` then adds the
reported secondary minutes without requiring a frozen main clock. Removing
only `stoppage_clock` from these saved observations changes rejection to
unverified. That is a labeled diagnostic control, not another model response
or authorization to alter the historical acceptance.

Other fresh responses read `95:xx` correctly but cannot verify it: the numeric
period fallback assigns ET1, while the event expects second-half stoppage.
The evaluator soft-keeps that period conflict. Several responses also invent
zero-minute secondary clocks; these do not change the integer minute but do
change clock provenance. Missing visible period labels must not be replaced
with an invented visual fact merely to force verification.

This is related to, but distinct from, FF-094's existing `Added`-field branch
that discards exact-minute precision. Future clock work must cover both the
announcement/secondary-timer extraction contract and safe running-clock
normalization. This pass establishes one fresh false rejection, not its match-day
frequency or the cause of the September 6 accepted results.

## Handoff and reproducibility

The bounded repair is feasible without changing acceptance policy: explicitly
retain the four never-public variants' fresh evaluations, preserve existing own
share acceptance, then commit restoration/routing/direct scores together and
publish the normal event update. Do not run a blanket historical overwrite.
Any production repair still requires its own plan, transactional guards and
explicit approval. Broad revalidation should wait for clock-boundary review.

Ignored local artifacts live under `scratch-audit-2026-09-13/maitland-niles-review/`.
The harness's `validate` mode refuses to overwrite prior output; `preview` has
no network or write-capable service clients. Its only writes are local reports.

- Snapshot SHA-256: `0be23df87c44b70a2e996966a6fbfde0529f7c9241b07e61f021022cd92db17d`.
- Preview SHA-256: `6b02fb382c09a10f84f814fb752123ca937077f9d4c068ba3e948a08ba262290`.
- Domain snapshot fingerprint: `4dd4ef33ac4a34a6b13e5ba898e4a3d44ad72e16d0af1ef9c2f0cd6f6e06528b`.
- Prompt SHA-256: `32fead6002006fc9acb8c6110a8d7d64c283a690ab335d7bbfa904687d0881b5`.
- Schema SHA-256: `00f178fc55b7fbd3a407eb83dbf28b1bed5d70b4d574ffd980bd9e7a34be763c`.
