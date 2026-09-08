# Pre-rollout production evidence — 2026-09-08

Read-only audit of work processed before the coordinated
[event-update rollout](../../history/event-update-rollout-2026-09-08.md).
Accepted work lives in the [issue register](../../todo.md), not this snapshot.
No production repair, replay, configuration change, model call, or synthetic
NATS publication was performed during this audit.

## Scope and evidence limits

- Main SQL window: `2026-09-01T00:00:00Z` through, but excluding,
  `2026-09-08T13:32:12Z`. Workflow totals use `started_at`, asset totals use
  `first_seen_at`, and failure totals use `outcome_at`.
- Runtime under investigation: pre-rollout `bad0bf7`. Current source is
  `9ea4fc9`, including deployed `3723ce2`; the investigated validation,
  comparator, and provider-shadow paths are unchanged by that rollout.
- Sources: PostgreSQL read-only transactions, retained Loki worker/gateway
  logs, recorded Temporal activity inputs, current code, and offline replay of
  the existing quality-audit export.
- Temporal namespace `default` retains completed execution history for
  **86,400 seconds**, with history and visibility archival disabled. The
  September 4 histories are no longer available. Loki is configured for
  30-day retention and supplied the incident logs. Missing old Temporal
  history is therefore expected, not evidence of lost running workflows.
- Current database rows show retained end state, not every intermediate
  provider observation. Gateway logs are shared-model evidence; Found Footy
  does not carry the gateway request ID through its failure records, so this
  audit cannot attribute every gateway request to a specific candidate.

## Workflow outcomes

All 252 downstream checklist rows started in the main window are terminal:

| Outcome | Workflows |
|---|---:|
| Assets surfaced | 154 |
| Candidates found, no assets | 53 |
| No candidates | 24 |
| Event removed | 17 |
| Twitter unavailable | 4 |

These are workflow outcomes, not fixture success rates or proof that every
real goal received a correct video. In particular, a completed workflow may
contain failed candidates.

## FF-087: vision failure burst and missing durable cause

PostgreSQL contains **167 `failed/vision_error` candidates across ten events**,
all with JSON-null `outcome_detail`. Their terminal timestamps span September 4
19:14:14–20:45:57 UTC. Retained workflow measurements identify **80 distinct
representative candidates**; the other 87 outcomes are exact-byte followers.
Do not count those followers as independent model attempts.

| Fixture | Scorer / minute | Failed candidate outcomes |
|---|---|---:|
| Ipswich–Liverpool, `1557393` | Isak 6′ / 9′ | 34 / 32 |
| Genoa–Como, `1550112` | Diao 30′ / 78′ | 27 / 1 |
| Genoa–Como, `1550112` | Baturina 25′ / Osmajić 19′ / Paz 40′ | 24 / 14 / 10 |
| PSG–Monaco, `1552753` | Nazinho 58′ / Marquinhos 31′ | 10 / 7 |
| Stuttgart–Köln, `1575149` | El Khannouss 39′ | 8 |

Worker log query window 18:30–21:15 UTC found **319 `llm_chat_failed` calls**
from 19:12:08 through 21:04:57. Every recorded cause was a 60-second
`context deadline exceeded` request to `control-joi.luv`. SDK records account
for the same 319 errors as 90 ordinary activity errors and **229 activity
completions reported after timeout**. That last count is attempts, not 229
additional lost clips. Final representative vision-stage durations ranged from
186,939 to 547,814 ms; the median was 546,030 ms, close to three full
three-minute attempts.

Gateway logs for 19:00–21:30 UTC show **97 Gemma admission waits exceeding
`SLOT_MAX_WAIT`**, classified `cold_long_prefill`, at approximately 60 seconds.
They also show 272 successful HTTP-200 dispatch records in that window;
recorded `waited_ms` reaches 59,975 and `ms` reaches 73,339. This is evidence
of admission pressure and slow responses, not a complete gateway outage.
The exact per-request breakdown between gateway waiting, backend work, and
Found Footy activity timeout remains uncorrelated.

Code explains why the current record cannot settle that breakdown:

- [LLM client](../../../internal/infra/llm/client.go): the local semaphore wait
  consumes the activity context; the separate 60-second request timeout begins
  only after local admission.
- [Pipeline activity options](../../../internal/workflow/event_pipeline.go):
  each vision attempt has a three-minute start-to-close limit and three
  attempts. Fetching bytes, probing, extracting frames, local admission, and
  remote work share that budget. Heartbeats do not extend start-to-close.
- [Vision callback](../../../internal/workflow/event_pipeline_validation.go):
  the exhausted error becomes `failExactCluster(c, "vision_error", nil)`.
  This loses both stage/class and Temporal timeout subtype before PostgreSQL.
- [Vision activity](../../../internal/activity/vision/activities.go): permanent
  model/configuration failures are already non-retryable; that is not the
  cause shown in this incident. Rejected or failed staging is deleted.

Diao 30′ (`e8664716-402c-4bec-b843-786abad7dc69`) ended with no accepted asset:
172 candidates, comprising 139 rejected, six download failures, and 27 vision
failures arising from 15 representatives. The other nine affected events have
an active share in the retained database. We lost validation opportunities;
we have **not** established that any failed Diao candidate was a valid goal
video. The broad query also found unrelated material.

**Next slice:** carry bounded vision stage/class and Temporal timeout subtype
through retry exhaustion and follower propagation, using FF-060's existing
failure-detail approach. Add separate local-wait/request timing, not raw
prompts or signed URLs. Test retryable failure, permanent failure, heartbeat
and start-to-close timeout, and exact followers. This improves diagnosis; it
does not by itself repair saturation. FF-037 owns the admission/work-lane
follow-up. Share the exact gateway window with Control before changing shared
admission, cancellation, or deadline policy. Do not blindly increase timeouts
or bypass vision.

## FF-088 and FF-075: postponed polling pollutes circuit evidence

FC Cincinnati–DC United (`1490439`) was scheduled for September 5 at
23:30 UTC and activated at 23:25. At the September 8 14:06:30 poll it was
still `active`, with `pst / Match Postponed`, no events, and no terminal
observation. This is an unfinished fixture, not a completed fixture stuck
behind the 30-minute terminal grace.

Recorded activity input from
`active-poll-scheduled-2026-09-08T13:47:00Z`, run
`560ad1ee-205c-47ca-a6ba-a1728a5c4739`, confirms the adapter observation:

| Fact | Stored fixture | Fresh observation |
|---|---|---|
| Home / away / league ID | 2242 / 1615 / 253 | unchanged |
| Status | `pst` | `pst` |
| Elapsed / extra | null / null | null / null |
| Home / away score | 0 / 0 | null / null |
| Trackable events | none | none |

[UpdateFromPoll](../../../internal/domain/fixture/state.go) retains the old
score when the new value is null; [RefreshActivePoll](../../../internal/infra/pg/fixture_repo.go)
writes that retained value. The [integrity evaluator](../../../internal/domain/providerintegrity/evaluate.go)
compares the retained score against null again on every poll and flags
`populated_field_cleared`. The repeated warning is not new independent
regression evidence.

This already contaminates the global **shadow recommendation**. At September 5
23:55:00, run `bac64daa-e635-4c8e-ae07-ff5add26c569`, Cincinnati and fixture
`1490437` each had only `populated_field_cleared`; the aggregate was
`positive_only / multiple_fixture_regressions`, with zero missing confirmed
events. The threshold is two anomalous fixtures. **No circuit enforced that
recommendation:** durable circuit/quarantine state and enforcement remain
unimplemented.

Polling has a separate lifecycle gap:

- [APIStatus.Live](../../../internal/domain/fixture/fixture.go) includes PST
  for short delays. Active membership has no time-bounded deferred policy.
- `Fixture.Reschedule` exists, but only tests call it. No runtime transition
  uses it to release a long-postponed fixture from active polling.
- Simply moving this row to staging is insufficient: `ShouldActivateNow`
  accepts a kickoff in the past; [staging polling](../../../internal/activity/monitor/activation.go)
  and [ingestion](../../../internal/activity/ingest/categorize.go) also
  emergency-activate `Live()` statuses, including PST.
- The comment that adding PST has zero request cost is conditional, not
  generally true. When this is the only active fixture it drives a by-ID call
  every 30 seconds: approximately 2,880 calls per day.

**Required before enforcement:** distinguish a stable deferred null-score
observation from a fresh destructive regression, while preserving guards for
missing established goals, nonzero scores, and played clocks. Pin both the
isolated case and the two-fixture aggregate as regressions. FF-088 separately
needs an explicit deferred polling/reactivation policy across active polling,
staging, and ingestion. Retain the fixture and its resumption path; do not
force-complete or delete it as a workaround.

## Existing failure classes and coverage gaps

- **FF-060 diagnostic validation:** all 1,149 `download_error` outcomes across
  148 events durably retain `cdn_download/forbidden`. The classification fix
  is validated; the CDN denial itself is not fixed. FF-089 carries that
  separate recovery investigation.
- **FF-038:** four workflows exhausted the unavailable-feed budget after
  11–14 usable observations. Their retained evidence shows X search HTTP 429,
  limit 50, remaining zero. FF-061 correctly separates unavailable attempts
  from usable searches; the finite outage budget still bounds coverage. This
  is separate from the video-CDN HTTP 403 failures.
- **FF-076:** four named goals remained at debounce zero with no workflow
  because their provider player ID was null: HEBC–Dortmund, J. Wrede 34′
  (`6e519bd8-fe21-4923-adad-eceac0657b6b`); Udinese–Venezia, Bayo 34′ and 53′
  (`38222e00-30e8-45d0-914a-4bce3a389d24`,
  `7e703805-4826-43b6-b493-0538e2115583`); Lens–Lorient, Mamadou Kone 54′
  (`83738bd1-9eed-4860-9698-b1cd918530b5`). Bologna–Sassuolo also has the
  named Tedesco 90′ card without an ID; do not assume that entry describes a
  player rather than staff. The identity-v2 proposal remains separate work.

## FF-081/082/083: quality evidence now supports the next review

Main-window assets: **590 across 163 events; all 590 retain frame rate**.
There are 311 retired variants, including **184 that never had a public
share**. Candidate attribution retains 2,561 exact observations; 1,634 point
to observed bytes different from the credited canonical asset. The evidence
collection is working without changing public keeper policy.

Of 311 direct supersession edges with cadence on both endpoints, **79 keepers
have less than 90% of their predecessor's reported frame rate**. That is a
review shortlist, not 79 proven quality mistakes. Duration, crop, overlays,
resolution, compression, and actual versus reported cadence still matter.

The full retained offline export, including older history, contains 2,409
assets across 748 events and 2,030 current direct match edges. It finds 32
arrival-sensitive components and the same one persisted legacy Danso cycle;
those totals must not be read as 32 new September regressions. There are
978 pairs with known cadence on both sides; the cadence-aware experiment
changes 128 decisions versus the prior stable-offset experiment. The full
direct-cover experiment would retain 889 assets versus 503 historical terminal
assets in matched components. This is still too large a product-policy change
to adopt without visual labels.

The analyzer's unconditional legacy limitation lines about unknown frame rate
and missing first-loss variants do not describe all new rows. Fix those report
labels before using the report as a standalone artifact; the counts and CSV
retain the new fields correctly.

### Preserved local review material

Ignored local directory: `scratch-audit-2026-09-08/` at the repository root.
It contains the full CSV export, generated report and review CSV, filtered
incident logs, recorded postponed input, and a five-pair media shortlist.
This is a local research copy, not a backup service or a production retention
exception. Do not delete it before the visual review; media is not committed.

The five pairs cover El Khannouss 39′, Che Adams 82′, Cesar Palacios 42′,
Tyrick Mitchell 35′, and Mariano Díaz 36′. Each preserves a never-public
approximately-60-fps variant and its directly selected 30-fps keeper. Selection
uses one pair per event and prefers small objects to bound the copy; it is not
a representative random sample. All ten exact S3 objects downloaded, totaling
17,303,120 bytes; sizes and database MD5s matched. SHA-256 checksums are in
`media-sha256.txt`. Public share redirects were not used because a retired
share may resolve to a different encoding.

| Artifact | SHA-256 |
|---|---|
| `retained-quality-corpus.csv` | `0b4b20df9fbe4636ab80663dfb4f0a717756a82dbf75af4d31e0ba48181623b0` |
| `quality-review.csv` | `232f8b3ee5e92e688ebadac66a01cdf8071de96fa8f6f3c904fab59852e31cca` |
| `media-sha256.txt` | `b285114c2ca7bc9ba3b123de3d3b7212348474342374b746f315392f5234b5dc` |

The [existing audit tool](../../../scripts/audit_video_quality/README.md)
reproduces the report from the export. It ran successfully with
`-max-permutations 100000 -details 12`, then `-review-csv`, in a disposable
2-CPU/4-GiB container. No human quality labels were invented and no comparator,
dHash threshold, or visibility rule changed.

## Recommended order

1. Implement FF-087's bounded durable failure evidence; use the retained
   admission evidence for a scoped FF-037/Control follow-up, not a blind
   timeout increase.
2. Repair FF-075's deferred-observation classification before enforcement;
   agree FF-088's polling/reactivation contract before changing lifecycle.
3. Resume the already-proposed FF-076 identity work. The new data confirms
   real discovery coverage loss, not merely a frontend naming defect.
4. Review the preserved FF-081 pairs before changing keeper selection. Keep
   FF-089 CDN recovery and FF-038 search admission distinct.

In parallel with those next decisions, FF-085/086 remain deployed and
`validating`: producer/BFF/SSE checks do not replace a natural event's receipt
and application in React, including no-clip `searching -> complete`.
