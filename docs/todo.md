# Active work and issue register

This file is the canonical project backlog. It owns current bugs, deferred
work, and audit findings that still need verification. Point-in-time audits
under [`design/`](./design/) are evidence snapshots, not live task lists.

Code remains the authority for current behavior. Before promoting an audit
claim into the confirmed backlog, reproduce it or verify the cited path
against the current branch.

## Working rules

- Work one issue at a time from **Next** unless production evidence changes
  the priority.
- Give every accepted issue a stable ID. Do not reuse IDs.
- Keep evidence concrete: fixture/event/workflow IDs, timestamps, code paths,
  or a deterministic test.
- A fix is complete only when code, regression tests, and the affected
  as-built documentation land together.
- Production deployment and production data repair are separate operations;
  each still requires explicit user approval.
- Move completed entries to a dated closed section instead of deleting the
  history.

### Status and severity

| Value | Meaning |
|---|---|
| `next` | The single issue selected for implementation. |
| `confirmed` | Reproduced or verified against current code; ready to schedule. |
| `triage` | Preserved from an audit but not yet re-verified against current code. |
| `mitigated` | Still present in code, with an operational guard reducing current impact. |
| `implemented` | Code, regression tests, and docs are complete locally; rollout or commit still remains. |
| `blocked` | Requires a decision or external dependency before implementation. |
| `P0` | Active outage, corruption, or broad clip loss. |
| `P1` | User-visible correctness failure or material resource/lifecycle leak. |
| `P2` | Bounded failure-state bug, operability gap, or performance debt. |
| `P3` | Cleanup or improvement without a current correctness failure. |

## Next

| ID | Severity | Status | Summary |
|---|---|---|---|
| FF-018 | P2 | `confirmed` | Correct the production Twitter reauthentication command so it names the explicit production Compose file. |

## Confirmed issues

### FF-001 — Firefox fleet is not environment-scoped

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Observed:** 2026-08-16, shared Docker daemon on luv.
- **Invariant:** A dev worker must never count, release, or reap a prod browser,
  and prod must never act on a dev browser.
- **Evidence:** Fleet containers carry only the global
  `found-footy.fleet=firefox` label plus event ID. `count()`,
  `ListInstances()`, and `ReapOrphans()` filter only the global label. At the
  11:30 UTC sweep, dev reaped the two prod event browsers while prod reaped
  two dev event browsers.
- **Current mitigation:** The Found Footy dev Compose stack remains down. The
  running production workers still contain the old unscoped implementation.
- **Resolution:** Compose selects `FIREFOXFLEET_NETWORK`; Go treats that opaque
  network identity as the ownership scope. Dynamic containers use scoped
  daemon names in workspace order
  (`<project>-<env>-firefox-ev-<full-event-uuid>`) and labels, while the old
  event-only hostname survives as a network alias for Temporal compatibility.
  Count, list, release, and reap require the scope and network; mutation
  verifies ownership first.
- **Verification:** The in-memory daemon regression provisions the same event
  ID in dev and prod scopes, then proves independent capacity, listing,
  reaping, and release. A foreign-ownership test proves name collision does not
  grant deletion authority. Full `make test-short`, `make vet`, dev Compose
  validation, and `git diff --check` pass.
- **Deployment gate:** Immediately before the first production rollout, verify
  no legacy `ff-firefox-ev-*` container is active on the prod network. Any
  legacy removal and the production rollout each require explicit approval.
- **Source relation:** The 2026-08-13 audit found the earlier absence of a
  reaper and state-blind capacity count. It did **not** find this cross-
  environment collision, which became possible after the reaper shipped.

### FF-002 — failed video child leaves candidate pending

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Observed:** 2026-08-16, Huijsen goal in Schalke–Real Madrid, event
  `b7fe0d77-832d-4664-8aba-0f78b1ca3c7e`.
- **Invariant:** Every persisted candidate must reach one terminal outcome,
  including when its child workflow fails. Any staged object must be promoted
  or reclaimed.
- **Evidence:** Two candidates downloaded the same 108,216,129-byte,
  3808×2146, 44.6-second file (`md5=729b89b8c5817c542266493130629019`).
  Both `HashVideo` activities exhausted three retries on
  `ffmpeg: extraction timeout`. Their child workflows failed, the parent
  completed, and both candidate rows remained `pending`.
- **Cause:** `pipeline.onVideoDone` decrements `inFlight` and returns when
  `Future.Get` fails. The failed future does not populate the output's tweet
  URL, so `RecordCandidateOutcome` is never called. The parent also lacks the
  staged key needed for cleanup.
- **Implemented locally; not deployed:** Exhausted download and hash activities
  now return typed child output with `failed`, a stable stage reason, the tweet
  URL, and any staging key. The parent stamps the candidate and deletes
  hash-failure staging. Its callback captures the input URL for an unexpected
  child failure and rejects invalid output explicitly. Cancellation remains an
  error and schedules none of this work. Temporal change version 1 protects
  existing EventWorkflow and VideoWorkflow histories; default-version replay
  retains the old command sequence.
- **Regression:** Child tests exhaust four download or three hash attempts and
  assert typed output plus staging correlation. Parent tests assert the exact
  failed outcome, hash cleanup, unexpected-child URL fallback, no vision call,
  and the default-version replay path.
- **Production follow-up:** The two Huijsen candidate rows and any surviving
  staging objects predate the fix. Inspection and repair remain separate
  explicitly approved production actions.
- **Source relation:** New finding. Prior audits discussed `HashVideo`
  heartbeats and promoted-object staging leaks, not this terminal-state path.

### FF-003 — exact candidate bleeds across fixture events

- **Status:** `confirmed`
- **Severity:** P1
- **Observed:** 2026-08-16, Lens–PSG fixture `1546791`.
- **Invariant:** A short exact video representing one match moment must not be
  surfaced for two distinct events merely because both searches return it.
- **Evidence:** Tweet `https://x.com/FH4A/status/2089071008082784644`
  and `md5=059e019aafd963d208782d35e8d1eb12` were promoted as unverified
  assets for both Thauvin 32′ and Antonio 39′. The file is 11.9 seconds, so it
  cannot contain both events seven match-minutes apart.
- **Constraint:** Event-scoped fuzzy dedup remains intentional; earlier
  cross-event perceptual dedup collapsed distinct goals. This issue concerns
  exact candidate/byte ownership and validation, not a return to blanket
  cross-event fuzzy matching.
- **Required work:** Design the assignment invariant before coding. Evaluate
  fixture-scoped exact tweet/MD5 ownership, clock-verdict precedence, and the
  treatment of genuine compilation tweets. Add a two-event regression.
- **Source relation:** Cross-event dedup was discussed and rejected on
  2026-07-25. This exact-identity bleed through unverified promotion was not
  identified by the audits.

### FF-004 — Lens clips evade perceptual dedup

- **Status:** `confirmed`
- **Severity:** P1
- **Observed:** 2026-08-16, Thauvin 32′ event
  `1be4d2a5-961f-4cfb-91c9-ce7558017ec0`.
- **Invariant:** Re-encodes of the same broadcast clip should consolidate
  without raising thresholds enough to merge different footage.
- **Evidence:** Three user-visible clips appeared to be the same camera angle
  with grading differences. Production requires a 30-frame window with at
  least 27 frames at Hamming ≤10. The minimum thresholds needed for the three
  pairs were 27, 32, and 31; their longest production-threshold windows were
  only 4, 3, and 3 frames.
- **Category detail:** Rank 1 was verified; ranks 2 and 3 were unverified, so
  production did not compare rank 1 across the category boundary. Ranks 2 and
  3 were compared and still failed at the hash layer.
- **Required work:** Preserve the three frame-hash sequences and representative
  frames as a regression corpus. Determine whether crop/overlay, color
  transformation, or temporal drift causes the distance. Do not raise the
  threshold toward 31: prior calibration places different footage around 23.
- **Source relation:** New live calibration finding; no prior audit contained
  this sample or failure measurement.

### FF-005 — high-resolution dense hash extraction times out

- **Status:** `confirmed`
- **Severity:** P2
- **Observed:** Same Huijsen candidates as FF-002.
- **Invariant:** A clip that passes download and metadata gates should either
  hash within its bounded budget or receive a deterministic terminal reason
  without repeating an operation that cannot succeed.
- **Evidence:** The same 3808×2146, 44.6-second file timed out in dense frame
  extraction three times for each of two candidate children.
- **Required work:** Profile the single-pass full-resolution PNG stream,
  ffmpeg semaphore wait, and dense timeout. Evaluate downscaling/equalization
  in the extraction path while preserving hash semantics. Classify a proven
  deterministic extraction timeout as non-retryable.
- **Source relation:** The 2026-08-13 audit identified heartbeat coverage and
  shared-semaphore contention. It did not demonstrate this post-heartbeat 4K
  failure.

### FF-006 — promoted clips retain staging objects

- **Status:** `confirmed`
- **Severity:** P1
- **Source:** 2026-08-13 P2-4, elevated to P1 by the 2026-08-15 audit.
- **Evidence:** Promote copies `staging/` to `assets/`; the success path never
  calls `DeleteStaging`. Other terminal paths do.
- **Required fix:** Delete staging after durable promotion, idempotently, and
  add a bounded stale-staging sweep as a backstop. Coordinate with FF-002 so
  every terminal path has an owner.

### FF-007 — abnormal EventWorkflow closure can strand a fixture

- **Status:** `confirmed`
- **Severity:** P1
- **Source:** 2026-08-15 audit, finding #196.
- **Evidence:** A workflow that closes before `MarkDownstreamComplete` leaves
  its checklist row open. Spawn recovery uses duplicate rejection and cannot
  re-drive the same workflow ID; fixture completion can remain blocked and
  the Firefox instance can remain pinned.
- **Required work:** Define recovery policy for failed/timed-out executions,
  make re-drive idempotent, and add an active-fixture maximum-age backstop.

### FF-014 — score-consistent goal is false-removed on event-array omission

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Observed:** 2026-08-16, Lazio–Mantova fixture `1564801`, final score
  0–2. The second goal was I. Cajazzo at 90+6, event
  `ce3eb72e-4964-4410-85d9-5a2d6628ce7a`.
- **Invariant:** A goal must not be classified as VAR-removed while the
  aggregate score still requires it. A played fixture must not complete while
  its non-shootout scoring-event inventory is inconsistent with the official
  per-team score.
- **Evidence:** The event was detected at 21:13:30 UTC, reached debounce count
  3, and started `EventWorkflow` at 21:14:30. Later API polls omitted Cajazzo
  from the event array while retaining the 0–2 score. Three absence votes
  reduced the event from 3 to 0 at 21:20:00, stamped `removed_reason='var'`,
  closed its discovery checklist as `event_removed`, and canceled discovery
  after attempt 6 of 15. The fixture completed in that same cycle with
  `completion_counter=3`, one surviving goal event, and a 0–2 stored score.
- **Cause:** `ReconcileFixture` treats every missing natural key as removal and
  explicitly assumes the provider event array is cumulative. The removal
  transaction closes pending downstream rows, so the false removal erases both
  completion blockers. `FixtureReadyToComplete` checks terminal debounce,
  event debounce, and pending workflows, but not score/event consistency.
- **Implemented locally; not deployed:** The same-response score/event
  inventory now guards goal absence votes. A score that still requires an
  omitted goal holds the stored event without decrementing it. For played
  terminal statuses (`FT`, `AET`, `PEN`), the fixture completion counter only
  advances when that response's scoring events exactly match its reported
  per-team score; a mismatch or nil score resets the counter. Winner flags no
  longer bypass the three coherent votes. The final gate still requires stored
  score parity, settled events, and completed downstream rows. Exceptional
  terminal statuses keep terminal-only voting. See the
  [decision record](./decisions/2026-08-16-score-backed-goal-removal.md).
- **Regression coverage:** Unit tests cover true score-correcting VAR, nil-score
  behavior, own-goal beneficiary attribution, missed-penalty and shootout
  exclusion, scorer replacement, exact/deficit/excess provider inventory,
  exceptional terminal states, and winner non-bypass. The Lazio scenario holds
  the goal through three inconsistent terminal polls, then requires three
  restored coherent polls before completion. `make test-short` and the full
  `make test` suite pass.
- **Deployment and repair:** The existing production row will not self-heal
  because fixture `1564801` is already completed and no longer polled. Deploy
  the guard first, then repair that fixture as a separate explicitly approved
  production operation.
- **Source relation:** Promotes and broadens the 2026-08-13 audit's P2-15
  sub-threshold-blip finding. That audit did not identify a confirmed goal
  disappearing while the aggregate score remained unchanged.

### FF-015 — canceled EventWorkflow spins into Temporal deadlock detection

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Observed:** The false-removal in FF-014 canceled workflow
  `event-ce3eb72e-4964-4410-85d9-5a2d6628ce7a`, run
  `f95770d4-79f0-496c-ab6c-208e397c254c`, during the 60-second wait after
  search attempt 6.
- **Invariant:** Cancellation at any producer or consumer yield point must
  terminate the workflow promptly and deterministically without a busy loop,
  workflow-task panic, or activity scheduled after cancellation.
- **Evidence:** Both production workers repeatedly reported `TMPRL1101`
  (`workflow goroutine "root" didn't yield for over a second`) for the same
  run. Workflow-task attempts continued through at least attempt 12.
- **Cause:** The search producer discards the error from `workflow.Sleep` and
  the pipeline consumer discards the error from `workflow.Await`. Once the
  workflow context is canceled, `Await` returns immediately while
  `searchDone` can remain false, creating a tight loop.
- **Resolution:** Producer activity and timer cancellation now propagate across
  the producer/consumer boundary; a deferred close sets producer completion on
  every exit. The consumer returns the blocking `workflow.Await` error instead
  of re-entering Await on an already-canceled context. Cancellation bypasses
  normal finalization because the monitor removal transaction and destroy path
  own checklist closure and resource cleanup.
- **Regression coverage:** Workflow tests cancel during attempt spacing, while
  `SearchTweets` is pending with no selector future, and while a
  `VideoWorkflow` child or vision activity is pending. Each completes as
  canceled after one search attempt, schedules no later attempt, and does not
  call `MarkDownstreamComplete`; the vision case also proves that no forensic,
  persistence, or staging-cleanup activity is scheduled after cancellation.
- **Source relation:** New live finding. It is independent of the false-removal
  policy: a genuine VAR follows the same cancellation path and can trigger it.

### FF-019 — production images do not carry verifiable build identity

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Observed:** 2026-08-16, live production worker metrics and the current
  production build configuration.
- **Invariant:** Every running production binary must identify the exact source
  revision and build time that produced it.
- **Evidence:** The live workers expose
  `found_footy_deploy_git_sha_info{binary="worker",git_sha="unknown",image_tag="",built_at="unknown"}`.
  Both worker containers were created at 10:46:33 UTC, 61 seconds after the
  current HEAD commit, but timing can only suggest the deployed revision; it
  cannot prove it. The API and Twitter containers were built at earlier times.
- **Cause:** Both production Dockerfiles accept `GIT_SHA` and `BUILT_AT`, but
  `docker-compose.prod.yml` passes only `BINARY`/`WITH_VNC`. No release command
  supplies the identity arguments or `IMAGE_TAG`. The Twitter command also
  discards its injected identity instead of exposing any verification surface.
- **Implemented locally; not deployed:** `make deploy-prod` resolves the full
  SHA of the clean checkout and one UTC build time, rechecks the tree before
  mutation, and uses that SHA as the image tag across worker, API, Twitter,
  Twitter VNC, and `FIREFOXFLEET_IMAGE`. It refuses active production event
  browsers instead of creating a mixed-version fleet. After recreation it
  verifies both workers and API through the deploy-info gauge and Twitter plus
  an already-running VNC through `/status.build`. It never fetches, pulls,
  switches branches, cleans fleet containers, or mutates durable services.
- **Regression:** The Compose contract test parses the production model and
  requires all four application images plus the fleet image to carry the same
  release variables. The Twitter HTTP test requires all three build fields in
  `/status`. `bash -n`, synthetic `docker compose config --quiet`, and the full
  test suite cover the remaining release surface.
- **Source relation:** New release-audit finding. The design and deployment
  ledger describe build identity as shipped, but Compose never completed the
  contract.

### FF-020 — production release gate can miss active event browsers

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Observed:** 2026-08-16, local review of the FF-019 release command before
  its first production use.
- **Invariant:** An application rollout must not recreate workers or Twitter
  while a production event browser is executing a search against the current
  release.
- **Evidence:** The gate matched only the legacy daemon-name prefix
  `ff-firefox-ev-*`. FF-001 creates workspace-conventional scoped names such as
  `found-footy-prod-firefox-ev-<uuid>`, so later releases would not see them.
  The single check also ran before image construction and the permission smoke;
  an event browser provisioned during that interval could survive the worker
  recreation on the old image.
- **Implemented locally; not deployed:** The release command now selects
  running browsers by `found-footy.fleet=firefox` plus membership in the
  production network, independent of name generation. It checks once before
  build work and again immediately before the first mutation. The dynamic
  daemon name now follows `<project>-<env>-<role>` order; deterministic name
  lookup is still followed by label, event, scope, and network verification
  before any lifecycle mutation.
- **Regression:** Fleet tests pin the exact workspace-conventional name. The
  release-contract test rejects a legacy prefix selector, requires the label
  and network filters, and requires a second guard after build but before
  recreation.
- **Source relation:** New finding in the not-yet-deployed FF-019 release path;
  no production release used the faulty gate.

### FF-016 — worker can permanently lose Twitter after a startup race

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Current code; legacy issue #170.
- **Invariant:** A transient browser outage during worker startup must not
  disable discovery on that worker for its lifetime.
- **Evidence:** `twitter.NewClient` synchronously probed the shared service's
  `/health` endpoint. `cmd/worker` treated probe failure as optional startup
  degradation and left `discovery.Activities.Twitter` nil permanently. The
  production release recreates Twitter and both workers together, while the
  browser's initial authentication can outlast the client's ten-second probe.
- **Implemented locally; not deployed:** Client construction now validates only
  static configuration and performs no network I/O. Invalid configuration is a
  worker-startup error; remote readiness is evaluated by each search. The
  client is always injected into discovery, so later Temporal attempts recover
  automatically when Twitter or an event browser becomes ready.
- **Regression:** Construction succeeds against an unavailable address without
  making a remote call. A second test returns 503 for the first search, changes
  the same service to ready, and proves the next search succeeds through the
  original client.
- **Source relation:** The eager health-probe idea came from the rebuild design,
  but it modeled readiness as immutable construction state. The shipped
  per-event fleet and retrying activity boundary require live per-call state.

## Confirmed lower-priority backlog

| ID | Severity | Source | Summary | Completion condition |
|---|---|---|---|---|
| FF-008 | P2 | Audit 2026-08-15 | Worker `/scratch` has no orphan sweep after process/OOM failure. | Bounded startup or scheduled sweep with active-path exclusions and tests. |
| FF-009 | P2 | Audits 2026-08-13 P2-12 and 2026-08-15 | Temporal schedules are create-only and retention still passes a hardcoded 14 days. | Config value wired; Describe→Update reconciliation tested and documented. |
| FF-010 | P2 | Audit 2026-08-15 | Completed fixtures are not revisited for late assist backfill. | Bounded completed-fixture refresh policy with vendor-call budget and tests. |
| FF-011 | P2 | Audit 2026-08-15 | Popularity increments are not idempotent under activity retry. | Retry-safe vote accounting with an invariant test. |
| FF-012 | P2 | Audit 2026-08-15 | Permanent LLM failures such as 401/404/bad JSON are retried. | Typed non-retryable classification and Temporal retry test. |
| FF-013 | P2 | Audit 2026-08-15 | Schema guard verifies one fingerprint, not the existence of every object after interrupted initialization. | Verify required objects or adopt ordered migrations; test partial schema. |
| FF-017 | P2 | Current code; legacy #170-adjacent finding | Firefox can die while the Go Twitter service remains alive and unusable because no browser watchdog or fatal-exit path exists. | Make browser death restart or terminate the service, expose correct health, and cover the process-loss transition. |
| FF-018 | P2 | Current production Compose and repository layout | `/authenticate.reauth_command` advertises `docker compose --profile vnc up -d twitter-vnc`, but the repository deliberately has no default Compose file, so the command fails without `-f docker-compose.prod.yml`. | Set the production value to the explicit Compose-file command, cover the operator response in configuration tests, update the as-built ledger, and deploy with approval. |

## Audit intake requiring current-code validation

These findings are preserved so they cannot disappear, but they are not yet
accepted as current bugs. Their source IDs are stable while they remain in
triage. Validate each against HEAD before assigning an `FF-*` ID or
implementation time.

| Source ID | Area | Candidate finding | Status |
|---|---|---|---|
| `AUD-0815-MUTABLE` | ingest/monitor | Player/team display fields and fixture league/round/kickoff may not refresh outside ingest. | `triage` |
| `AUD-0815-FLEET-TOCTOU` | fleet | Capacity check and provision are not one atomic operation; safe only while callers serialize provisioning. | `triage` |
| `AUD-0815-SHARE-TOCTOU` | persist | Share mint has a check-then-write race under concurrent promotion. | `triage` |
| `AUD-0815-ROT` | code/docs | Dormant compatibility vocabulary and zero-caller functions remain after cutover cleanup. | `triage` |
| `AUD-0813-P1-2` | monitor | Positional natural-key sequence can misidentify a same-player brace after removal or API reorder. | `triage` |
| `AUD-0813-P1-4` | bootstrap | Metrics/health listener bind failure may let a binary exit cleanly or run without its listener. | `triage` |
| `AUD-0813-P2-1` | API | Fixture reads use N+1 event/video queries and the completed bucket is unbounded at the query layer. | `triage` |
| `AUD-0813-P2-3` | video | CDN-download HTTP 403 may be classified as terminal geo-restriction when retry could recover it. | `triage` |
| `AUD-0813-P2-5` | workflow | Serialized selector consumer blocks on persistence I/O that may not require serialization. | `triage` |
| `AUD-0813-P2-6` | Temporal | One task queue lets LLM semaphore waiters starve I/O-bound activities. | `triage` |
| `AUD-0813-P2-7` | fleet | Firefox provisioning runs sequentially inside the 30-second active-poll path. | `triage` |
| `AUD-0813-P2-8` | discovery | Forensic candidate persistence blocks child workflow spawn on the speed-to-clip path. | `triage` |
| `AUD-0813-P2-9` | ffmpeg | Dense hashing and latency-sensitive probe/frame work share one process lane. | `triage` |
| `AUD-0813-P2-11` | API/S3 | Redirect cache lifetime can equal the presign lifetime, creating boundary-expired playback URLs. | `triage` |
| `AUD-0813-P2-13` | observability | `calls_total{error_class}` can remain empty because emitted error fields do not populate that label. | `triage` |
| `AUD-0813-P2-16` | monitor | A removed natural key that reappears may be skipped or collide forever instead of being reconciled. | `triage`; related to FF-014 |
| `AUD-0813-P3-1` | vision | Unknown API minute (`0`) may reject a clock-bearing clip instead of retaining it unverified. | `triage` |
| `AUD-0813-P3-2` | video | A hash shorter than the configured dedup window can pass while being structurally unable to deduplicate. | `triage` |
| `AUD-0813-P3-3` | persist | Idempotent promotion retry may skip rank rebalance. | `triage` |
| `AUD-0813-P3-4` | ranking | Dedup winner selection and public ranking may use inconsistent quality metrics. | `triage` |
| `AUD-0813-P3-5` | monitor | Reconcile may fetch the same pending-event set twice per fixture per cycle. | `triage` |
| `AUD-0813-P3-6` | monitor | Discovery recovery may repeat duplicate start/register work every cycle for healthy workflows. | `triage` |
| `AUD-0813-P3-7` | eventing | Coincident active/staging polls may emit fixture activation twice. | `triage` |
| `AUD-0813-P3-9` | ingest | An empty tracked-team cache may still burn lookahead API calls whose results are discarded. | `triage` |
| `AUD-0813-P3-10` | aliases | National-team resolution may take the club branch after a soft profile failure. | `triage`; may be superseded by resolver removal |
| `AUD-0813-P3-12` | discovery | `max_age_minutes` has inconsistent fallback defaults. | `triage` |
| `AUD-0813-P3-13` | dedup | Workflow-replayed perceptual matching allocates per offset and lacks a cheap prefilter. | `triage` |
| `AUD-0813-P3-14` | ranking | Full rank rebalance runs after every promote and supersede rather than once per settled batch/event. | `triage` |
| `AUD-0813-P3-15` | ranking | Fully tied rank comparisons may lack a deterministic final tiebreaker. | `triage` |
| `AUD-0813-P3-16` | observability | S3 download byte accounting occurs on response creation rather than consumed/closed bytes. | `triage` |
| `AUD-0813-P3-17` | config/docs | Worker shutdown timeout documentation attributes the setting to the wrong mechanism. | `triage` |
| `AUD-0813-CF-153` | twitter ops | Full cookie expiry still requires an operator-capable raw-browser re-auth and verified fleet propagation path. | `triage` |
| `AUD-0813-CF-175` | coverage | National-team coverage may require an explicit seed beyond the configured league-derived roster. | `feature scope`; verify current contract |
| `AUD-0813-CF-179` | video delivery | `ffmpeg.Faststart` exists but has no caller, so staged and promoted assets retain the downloaded MP4 layout. | `triage`; measure playback impact before restoring the historical hard requirement |
| `AUD-0813-CF-SLO` | observability | Per-match coverage summary and SLO alert were dropped; telemetry storage remains unused. | `triage` |
| `AUD-0813-CF-SCORE` | data model | Events do not preserve the score snapshot at detection, so clients cannot derive “made it 2–1” reliably. | `triage` |
| `AUD-DESIGN-COVERAGE` | testing | The historical design called for per-package coverage floors, but current hooks enforce only compilation and passing tests. | `feature scope`; decide whether coverage floors are worth their maintenance cost |
| `AUD-DESIGN-LOG-CATALOG` | observability | The historical design proposed generated module/action documentation; current practice derives the catalog from vocabulary source. | `feature scope`; add only if source inspection becomes an operational burden |
| `AUD-DESIGN-TRACING` | observability | The tracing package is a no-op compatibility stub; no OTLP export or trace correlation is wired. | `feature scope`; design only when a concrete cross-service diagnostic need justifies it |
| `AUD-TWITTER-RATE-LIMIT` | twitter | The scraper has no explicit Twitter rate-limit/interstitial classification or per-instance backoff. | `feature scope`; validate observed failure modes before designing backoff |

The 2026-08-13 audit marked its P0 set, P1-1, winner propagation,
heartbeats, NATS push path, fleet fallback/reaper, and several carry-forward
items closed. The 2026-08-15 audit reconfirmed that closure set and promoted
the still-open items now represented by FF-006 through FF-013. The 2026-08-05
audit must not be intaken independently because the later audits already
reconciled it.

Explicit non-active dispositions from the 2026-08-13 table: P2-4 maps to
FF-006; P2-12 maps to FF-009; P2-15 is broadened by FF-014; P3-8 duplicates
P1-2; and P3-11 was superseded when the alias resolver was removed. P0-1
through P0-5, P1-1, P1-3, P2-2, P2-10, and P2-14 are recorded closed by the
audit's post-fix evidence and dated decisions. Revalidate that closure only if
new production evidence contradicts it.

## Documentation work

| ID | Severity | Status | Work |
|---|---|---|---|
| DOC-001 | P1 | `implemented` | Reconciled the as-built ledgers after the alias-resolver teardown and Go cutover. Retired claims and copied inventory counts were removed from current-system sections. |
| DOC-002 | P2 | `implemented` | The former monolithic decision log is frozen in `docs/decisions/archive-through-2026-08-16.md`; `docs/decisions/README.md` owns the forward one-file-per-decision convention and `docs/decisions.md` preserves heading-link compatibility. |
| DOC-003 | P2 | `implemented` | `docs/operations.md` now defines environment lifecycle, production mutation gates, routine inspection, SQL and Temporal diagnosis, cookie re-auth, fleet ownership checks, failure recovery boundaries, and rollout/rollback gates. |
| DOC-004 | P3 | `implemented` | Rebuild roadmaps, proposals, and audits are routed as historical evidence. Live runtime and API contracts were extracted into current as-built docs without rewriting historical conclusions. |
| DOC-005 | P1 | `implemented` | Surviving audit, test, dev, production, and current-ledger findings now have one disposition: confirmed FF ID, stable triage ID, feature scope, duplicate/superseded mapping, intentional behavior, or later-audit closure. |
| DOC-006 | P1 | `implemented` | Post-cutover authority now starts with code, focused as-built ledgers, decisions, and this issue register. `AGENTS.md`, routing, the rebuild-plan banner, and the decision log demote the plan to historical target evidence. |
| DOC-007 | P2 | `implemented` | Every documentation area is routed as current authority, historical evidence, frozen legacy reference, or retired duplicate. Relative links were checked after the moves and deletions. |

### Documentation normalization acceptance criteria

- Every active or candidate issue has one stable identifier, source, status,
  evidence, and completion condition. The same issue is not active in an audit,
  roadmap, proposal, and tracker simultaneously.
- `docs/README.md` reaches every current source of truth in one hop. Historical
  files carry an unmistakable banner and point back to the current authority.
- Current ledgers describe only shipped behavior. Plans and proposals describe
  historical intent and never act as a second implementation backlog.
- Each retained document has a distinct job. Duplicate walkthroughs and stale
  status surfaces are consolidated after their unique information is moved.
- Files over roughly 500 lines are split or explicitly retained as frozen
  evidence/reference. Vendored API documentation and frozen legacy material are
  exempt from active-doc size expectations.
- All relative links resolve after moves or deletions. No material document is
  deleted until its inbound links and unique content have been checked.

## Behavior that is intentional

- Goals, red cards, and missed penalties all run the full discovery workflow,
  including the configured 15 Twitter search attempts.
- Perceptual dedup is event-scoped and category-scoped. Do not classify the
  lack of general cross-event fuzzy dedup as a bug without new evidence.
- The archived Python implementation is a behavioral reference, not the Go
  architecture template.
