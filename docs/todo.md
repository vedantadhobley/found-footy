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
| FF-018 | P2 | `implemented` | Correct the production Twitter reauthentication command so it names the explicit production Compose file; rollout remains. |

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

### FF-003 — unverified candidate can be attributed to the wrong event

- **Status:** `confirmed`
- **Severity:** P1
- **Observed:** 2026-08-16, Lens–PSG fixture `1546791`.
- **Invariant:** An unverified candidate should not be attributed to an event
  solely because a broad team term matched when stronger available evidence
  identifies another event. The same tweet or video may legitimately represent
  multiple events; cross-event reuse is not itself a defect.
- **Evidence:** Tweet `https://x.com/FH4A/status/2089071008082784644`
  (`md5=059e019aafd963d208782d35e8d1eb12`, 11.9 seconds) was promoted as
  unverified for both Thauvin's 32′ goal and Antonio's 39′ red card. Its Arabic
  text explicitly describes Antonio's red card. It appeared immediately after
  that event and Antonio's `(antonio OR Lens)` search found it on attempt 1.
  Thauvin's older `(thauvin OR Lens)` workflow found it on attempt 8 through
  the generic `#lens` match. With no readable broadcast clock, both workflows
  accepted it as unverified.
- **Cause:** Search intentionally permits team-only matches for recall. The
  video validator receives frames plus the expected minute, but not the stored
  tweet text, and it classifies football/screen/clock rather than semantic
  relevance to the event's player and type. Independent per-event workflows
  therefore have no evidence-resolution step when the clock is absent.
- **Constraints:** Keep event-scoped perceptual dedup; fixture-scoped exact or
  fuzzy uniqueness would incorrectly reject a goal-plus-card sequence,
  compilation, or another genuine multi-event clip. Do not implement
  first-claim-wins ownership.
- **Design directions:** Evaluate search-query precision separately from
  downstream validation. Pass tweet text and event context alongside video
  frames to multimodal validation; combine explicit player/event-type mentions,
  clock evidence, tweet-time proximity, and visual event semantics. Multiple
  event assignments must remain valid when evidence supports them; ambiguous
  candidates need an explicit confidence/fallback policy rather than forced
  exclusive ownership. Preserve this Lens sample as a two-event regression.
- **Rollout:** Design work only; this does not block the pending correctness and
  lifecycle rollout.
- **Source relation:** Cross-event dedup was discussed and rejected on
  2026-07-25. The audits did not identify this team-only search plus
  no-clock-validation path.

### FF-004 — Lens clips evade perceptual dedup

- **Status:** `confirmed`
- **Severity:** P1
- **Observed:** 2026-08-16, Thauvin 32′ event
  `1be4d2a5-961f-4cfb-91c9-ce7558017ec0`.
- **Invariant:** Re-encodes of the same broadcast clip should consolidate
  without raising thresholds enough to merge different footage.
- **Corrected sample:** Only ranks 1 and 2 are the Thauvin goal pair. Rank 3 is
  the Antonio red-card candidate described in FF-003; it should not deduplicate
  with goal footage, and its distances must not tune this matcher. The initial
  three-way comparison conflated attribution and dedup defects.
- **Evidence:** Rank 1 is a verified 59.9-second Sport TV clip; rank 2 is an
  unverified 15.3-second Just Football clip. Both source texts identify
  Thauvin's goal and both stored assets are 1280×720. Production requires a
  30-frame window with at least 27 frames at Hamming ≤10. This pair's longest
  qualifying window is only 4 frames and the minimum per-frame threshold needed
  for 27 of 30 frames is 27. Across every possible frame pairing, only 1 of
  rank 2's 152 frames has any rank-1 frame at Hamming ≤10, so integer-offset
  temporal drift is not the primary failure.
- **Category detail:** Rank 1 is verified and rank 2 unverified, so production
  intentionally did not compare them across the category boundary. The stored
  hashes show that a comparison would not have collapsed them anyway.
- **Transform checks:** Hash-only probes for contrast inversion, horizontal or
  vertical mirroring, and 180° rotation did not recover a match. The remaining
  likely class is a layout-changing crop/zoom/overlay or a color transform that
  the current grayscale-equalized full-frame dHash does not normalize. Stored
  hashes cannot distinguish those causes without representative frames.
- **Required work:** With approval to retrieve the two public source clips,
  preserve their frame-hash sequences and minimal representative frames as a
  regression corpus. Measure crop/layout and color-normalization variants before
  choosing a matcher change. Do not raise the global threshold toward 27: prior
  calibration places different footage around 23. Preserve category safety and
  treat cross-category consolidation as a separate evidence-policy decision.
- **Rollout:** Calibration work only; the safe current failure mode is an extra
  clip, so this does not block the pending rollout.
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
  extraction three times for each of two candidate children. The final attempt
  logged `extract_dense elapsed_ms=100045` and `signal: killed`, exactly the
  adapter's 100-second dense deadline. The adapter starts that elapsed timer
  after acquiring its semaphore, so this sample did not spend the measured
  interval waiting for admission.
- **Cause:** Dense extraction emits roughly 446 full-resolution lossless PNGs.
  For every 8.2-megapixel frame, Go then decodes PNG and performs full-frame
  grayscale conversion, histogram equalization, and area reduction before
  retaining only a 64-bit dHash. That work is resolution-dependent even though
  the dedup signal is 9×8. FF-021 host oversubscription aggravates it, and
  FF-022 repeats it for byte-identical candidates.
- **Required work:** Preserve the served MP4 at source quality, but normalize
  the separate hashing stream to a bounded grayscale working resolution before
  PNG serialization—or replace PNG transport with fixed-size raw grayscale
  frames. Re-baseline dHash thresholds against the existing transform corpus
  and the FF-004 live pair because preprocessing changes hash semantics. Keep
  timeout retries for genuinely transient failures; do not label all dense
  timeouts non-retryable when host contention can still cause them.
- **Rollout:** FF-002 makes exhaustion terminal and cleans staging, so this
  throughput fix does not block the pending rollout.
- **Source relation:** The 2026-08-13 audit identified heartbeat coverage and
  shared-semaphore contention. It did not demonstrate this post-heartbeat 4K
  failure or invalidate the full-resolution hashing cost assumption.

### FF-021 — per-replica ffmpeg caps overcommit the host

- **Status:** `confirmed`
- **Severity:** P1
- **Observed:** 2026-08-16 production topology during the Huijsen workflow.
- **Invariant:** A documented host CPU budget must remain a host budget when a
  worker is replicated.
- **Evidence:** Production runs two worker replicas. Each process reads
  `FFMPEG_MAX_CONCURRENT=32` and owns an independent 32-slot semaphore with one
  ffmpeg thread per process. The stack can therefore admit 64 ffmpeg processes
  on luv's 32 hardware threads. The 2026-08-06 decision and Compose comments
  calculate `32 × 1 = 32` as though one worker existed.
- **Impact:** Match flurries can oversubscribe CPU, stretch dense operations to
  their deadline, and create retries that add more work to the same bottleneck.
  The limit also governs fast probe and vision-frame work, so saturation delays
  latency-sensitive stages beyond hashing.
- **Required work:** Make the per-replica limit derive from one explicit stack
  CPU budget and pin the fixed production replica count in the same contract.
  Add a Compose test that multiplies replicas, per-process slots, and threads.
  Longer term, move dense work to a dedicated Temporal queue or shared
  admission controller if worker count becomes elastic; a process-local
  semaphore cannot enforce a host-wide invariant.
- **Rollout:** Correcting a prod-loaded limit is a separate production config
  action. Measure current peak concurrency before selecting the new budget.
- **Source relation:** Audit 2026-08-13 P2-9 found the shared fast/dense lane,
  but neither that audit nor the 2026-08-06 decision accounted for replicas.

### FF-022 — byte-identical candidates hash before the MD5 gate

- **Status:** `confirmed`
- **Severity:** P2
- **Observed:** Same Huijsen candidates as FF-002 and FF-005.
- **Invariant:** Once download has produced an exact content hash, only one
  candidate per event and MD5 should perform dense hashing and vision work.
- **Evidence:** The two different tweet URLs downloaded the same 108,216,129
  bytes and produced the same MD5, yet each `VideoWorkflow` ran and retried
  `HashVideo` three times. `DownloadAndStage` computes MD5 first, but
  `VideoWorkflow` does not return it to `EventWorkflow` until after hashing.
  The parent's `matchMD5` gate is therefore pre-vision but not pre-hash.
- **Impact:** Exact reposts multiply the most expensive local work and can
  amplify deterministic failures. The 2026-08-09 decision's statement that
  hashing is cheap and parallel is false for the live 4K sample.
- **Required work:** Introduce an orchestration boundary after
  download/fingerprint/stage. The event owner must claim `(event_id, md5)`
  before launching one hash continuation; duplicates transfer popularity and
  reclaim their staging objects without hashing. Preserve Temporal replay with
  a change version and test two simultaneous same-MD5 downloads, winner failure,
  and cancellation.
- **Rollout:** Optimization and failure-amplification fix; FF-002 contains its
  terminal-state consequences, so it does not block the pending rollout.
- **Source relation:** Not found by the audits. The as-built proposal documents
  that every child hashes, but production disproved its cost premise.

### FF-023 — promotion retry can skip rank rebalance

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Promotes audit 2026-08-13 P3-3 after current-code validation.
- **Invariant:** A retry must complete every durable step after a partially
  successful promotion, including rank repair.
- **Evidence:** `PromoteAndPersist` inserts a new share and then calls
  `RebalanceRanks`. If rebalance fails, the activity retries, finds the share
  inserted by the first attempt, and returns immediately without calling
  rebalance again. The retry therefore converts a transient error into a
  permanently stale rank order.
- **Resolution:** An existing share is now idempotent progress, not activity
  completion. `PromoteAndPersist` populates its output from that share, always
  rebalances ranks, then performs FF-006 staging cleanup. The final successful
  activity completion returns `Minted=true` because the workflow never
  observed the share created by the failed attempt and still owes its one
  dirty signal.
- **Regression:** The activity fake fails the first rebalance after share
  insert. The retry must skip a second copy and share mint, run rebalance
  again, delete staging, and report the durable share to the workflow.
- **Rollout:** Couple with FF-006 because both change the same promotion commit
  tail and retry boundary.

### FF-006 — promoted clips retain staging objects

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Source:** 2026-08-13 P2-4, elevated to P1 by the 2026-08-15 audit.
- **Evidence:** Promote copies `staging/` to `assets/`; the success path never
  calls `DeleteStaging`. Other terminal paths do.
- **Retry trap:** Deleting staging immediately before success is insufficient.
  If deletion succeeds but the activity completion acknowledgement is lost, a
  retry starts with `Copy(staging, assets)` and fails because its source is
  gone, even though the deterministic asset and share are already durable.
- **Resolution:** Derive the asset ID first and query it before copy. A retry
  that finds the deterministic asset skips copy, ensures the share exists,
  always rebalances ranks per FF-023, and idempotently deletes staging last.
  A first attempt still copies before inserting the asset, so an asset row
  continues to prove that destination bytes were written. A mismatched
  deterministic row fails closed instead of authorizing a retry against the
  wrong object.
- **Regression:** Activity tests cover happy-path cleanup, ordinary retry after
  cleanup, an uncertain delete response with a now-missing staging source, and
  immutable-identity mismatch. Each successful retry retains one asset and one
  share without a second copy.
- **Remaining resilience:** FF-024 owns a bounded orphan sweep for abnormal
  process or workflow termination. Normal successful promotion no longer
  depends on that sweep for cleanup.
- **Production follow-up:** Existing leaked staging objects predate this fix.
  Inspecting or deleting them remains a separate explicitly approved
  production action.

### FF-007 — abnormal EventWorkflow closure can strand a fixture

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Source:** 2026-08-15 audit, finding #196.
- **Evidence:** A workflow that closes before `MarkDownstreamComplete` leaves
  its checklist row open. The spawner used `RejectDuplicate` and a 30-minute
  execution timeout, so the same deterministic ID could never re-drive. Even
  if reuse were enabled alone, search progress, candidate ownership, and the
  live dedup pool existed only in workflow memory.
- **Implemented locally; not deployed:** Event starts now use typed
  `ALLOW_DUPLICATE_FAILED_ONLY` and no arbitrary workflow execution timeout.
  Each fully scheduled search attempt advances a monotonic checkpoint in the
  downstream row. A replacement run restores that checkpoint, all candidate
  URLs and their pending/terminal state, and active persisted assets. It
  re-drives pending candidates, excludes terminal candidates, and resumes at
  the first unfinished search attempt before it may close the checklist.
- **Regression:** Spawner unit tests lock the reuse/timeout/error contract;
  WorkflowTestSuite covers recovered attempts, candidates, and assets; a real
  Postgres integration test covers monotonic metadata and pending-state load.
  A Temporal version marker plus default-version test preserve the command
  sequence of executions started before the fix.
- **Remaining boundary:** This fixes closed unsuccessful executions. Temporal
  still reports a wedged execution as `RUNNING`, where failed-only reuse must
  reject a duplicate. FF-025 owns status-aware stale-run recovery. A blind
  fixture maximum-age force-complete is rejected because it can bypass score
  consistency and unfinished downstream work.

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

### FF-017 — Firefox death leaves the Go service alive and unusable

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Current code; archived Python recovery behavior; the superseded
  T/g assumption in the per-event scaling proposal.
- **Invariant:** Loss of the critical Firefox/Playwright child must make the
  service unhealthy immediately and recover the complete container unit before
  a later Temporal search retry.
- **Evidence:** Playwright launched Firefox below the long-lived Go PID 1, but
  exposed no lifecycle signal to the service or command. An OOM or browser
  crash therefore left HTTP running with a dead persistent context. Dynamic
  event containers also used Docker `restart: no`, so neither Compose nor the
  Docker daemon could replace the failed unit. The archived Python service
  detected a dead WebDriver and relaunched from the cookie backup.
- **Cause:** The scaling proposal declared the session watchdog subsumed on the
  assumption that a per-event browser crash would fail its container. The Go
  process did not actually exit, and raw Docker API children do not inherit the
  static Compose service's restart policy.
- **Implemented locally; not deployed:** Browser context close and browser
  disconnect converge on a one-shot critical-child signal. The service enters
  `failed`, `/health` returns 503, and `twitter.browser_failed` emits once. The
  command then exits PID 1 non-zero. Static headless Twitter retains Compose
  `unless-stopped`; dynamic event containers now carry Docker `on-failure` and
  reload the shared cookie backup on restart. VNC remains operator-controlled
  with `restart: no`. `SearchTweets` retries at roughly 0/10/30/60 seconds so
  the measured 30-second cold start is covered even on the final outer search.
  A Temporal version marker preserves the old retry attributes for existing
  histories. No application branch knows dev versus prod.
- **Regression:** Browser, service, and command unit tests cover one-shot
  signaling, failed health, single audit emission, and fatal process result.
  The in-memory Docker daemon test requires `on-failure` on every provisioned
  event container. A one-attempt workflow test fails three activity tries and
  requires the fourth to surface a candidate; its default-version companion
  preserves the historical three tries. The uncached affected-package run,
  `make test-short`, `make test`, and `make vet` pass.
- **Source relation:** This intentionally supersedes only the scaling
  proposal's “watchdog is subsumed” claim. It preserves the per-event fleet and
  delegates restart ownership to Docker rather than porting Python's in-process
  Selenium relaunch loop.

### FF-018 — production reauthentication command cannot resolve Compose

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Current production Compose and repository layout.
- **Invariant:** Operator instructions returned by `/authenticate` must be
  directly executable from the repository and must identify their environment.
- **Evidence:** Production set `TWITTER_VNC_START_CMD` to `docker compose
  --profile vnc up -d twitter-vnc`, but the repository deliberately has no
  default Compose file. The command therefore fails before it can resolve a
  service.
- **Implemented locally; not deployed:** Production now advertises `docker
  compose -f docker-compose.prod.yml --profile vnc up -d twitter-vnc`. Dev
  already named its explicit Compose file and is unchanged.
- **Regression:** The release-contract test parses the production Compose model
  and requires the exact environment-explicit command on the Twitter service.
- **Production note:** Until the rollout, ignore the command returned by the
  live endpoint and use the explicit production form documented in
  [`operations.md`](./operations.md#twitter-authentication-and-cookie-re-auth).

### FF-026 — metrics listener failure does not fail the binary

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Source:** Audit 2026-08-13 P1-4, revalidated against current bootstrap.
- **Invariant:** Application work must not run unless the binary owns its
  configured `/metrics` and `/healthz` socket. Losing that listener after
  startup must terminate the process as a failure.
- **Evidence:** Bootstrap launched `ListenAndServe` in a goroutine and buffered
  its error while calling `Work` synchronously. A bind error could therefore
  leave the worker running indefinitely without health or metrics. When
  `Work` later returned nil, bootstrap logged the listener error but ignored it
  when selecting the process exit status.
- **Implemented locally; not deployed:** Bootstrap now binds with `net.Listen`
  before it emits startup or calls `Work`. A bind error returns through the
  process exit boundary immediately. `Work` and the serving goroutine then run
  under one lifecycle select; a later listener failure cancels `Work`, drains
  registered adapters in LIFO order, and remains the returned fatal error.
  The public `Run` wrapper is bootstrap's only `os.Exit(1)` boundary, while the
  internal lifecycle returns an error and is directly testable.
- **Regression:** One test occupies an ephemeral address and proves `Work` is
  never called. A subprocess test requires the public `Run` boundary to exit 1.
  A third test binds an OS-assigned port, runs `Work`, and proves the metrics
  listener shuts down without deadlock. The uncached bootstrap and binary
  package tests, targeted race detector, `make test-short`, `make test`, and
  `make vet` pass.
- **Source relation:** This closes `AUD-0813-P1-4`. Its suggested non-blocking
  channel check was not used because it can race the listener's bind attempt;
  synchronous socket ownership provides a deterministic startup boundary.

### FF-027 — positional event sequence misidentifies a same-player brace

- **Status:** `implemented`; not deployed
- **Severity:** P1
- **Source:** Audit 2026-08-13 P1-2 and P3-8, revalidated against current
  monitor reconciliation; related P2-16 and P3-5 paths validated in the same
  code.
- **Invariant:** Removing or reordering one same-player event must not change
  another event's durable identity. A new event must not reuse any active or
  removed natural-key sequence.
- **Evidence:** Reconcile reset a per-response positional counter for each
  `(team, player, type)` group. If a player's first goal disappeared, their
  second goal changed from sequence 2 to sequence 1. The later row then received
  an absence vote while the removed key blocked or absorbed the survivor. The
  helper claiming to collect every natural key actually called `ListPending`,
  which excludes removed rows.
- **Implemented locally; not deployed:** `ListAllByFixture` returns the complete
  active and removed identity history. Within each scorer/type group, an
  order-preserving dynamic-programming match reuses active sequences by nearest
  effective match clock and detail; unmatched events allocate above the full
  historical maximum. Score-proven incomplete goal arrays require exact-clock
  matching to keep a nearby new goal distinct from an omitted one. Exact
  removed-row reappearances resolve to the terminal tombstone rather than
  generating a repeated unique-key error. Reconcile now uses this single
  history query instead of separate active and misleading pending reads.
- **Regression:** Monitor tests cover first-goal VAR in a brace, a third goal
  after the tombstone, reversed provider order, and a nearby new goal during an
  incomplete score/event response. A clock-correction test requires the
  original key to survive an ordinary one-minute adjustment. A real Postgres
  integration test proves the history query returns active and removed rows.
  Focused uncached tests, the targeted race detector, `make test-short`,
  `make test`, and `make vet` pass.
- **Decision:** [Event sequences match stored identity instead of provider
  array position](./decisions/2026-08-17-event-sequences-match-stored-identity.md).
- **Source relation:** Closes `AUD-0813-P1-2`, its P3-8 duplicate,
  `AUD-0813-P2-16`, and `AUD-0813-P3-5`. The natural-key format and terminal
  removal policy remain unchanged.

### FF-028 — cached video redirect can outlive its presigned target

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Audit 2026-08-13 P2-11, revalidated against current API and S3
  defaults.
- **Invariant:** The cache lifetime of a 302 must be strictly shorter than the
  presigned Garage URL embedded in it.
- **Evidence:** `RedirectVideo` fixed `Cache-Control` at `max-age=300`, while
  `S3_PRESIGNED_URL_TTL` defaults to the same five minutes. A cache hit near the
  boundary could return an already expired playback URL.
- **Implemented locally; not deployed:** API assembly passes the configured
  presign lifetime to the handler. Redirect caching subtracts a one-minute
  safety margin and caps at five minutes; the current default emits
  `public, max-age=240`. A lifetime that cannot supply the margin emits
  `no-store`. This remains correct under environment overrides without changing
  the presign default.
- **Regression:** Table tests cover the default, long, short, and unset
  lifetimes. The redirect handler test requires the derived header on an active
  share response. Focused uncached tests, `make test-short`, and `make vet`
  pass.
- **Decision:** [Video redirect cache stays inside the presigned URL
  lifetime](./decisions/2026-08-17-video-redirect-cache-stays-inside-presign.md).
- **Source relation:** Closes `AUD-0813-P2-11` while preserving the historical
  repeated-play cache benefit.

### FF-029 — CDN download 403 is mistaken for terminal geo-restriction

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Audit 2026-08-13 P2-3, revalidated against the current
  syndication adapter and video activity.
- **Invariant:** A CDN byte-download denial must not discard a candidate while
  retrying can resolve a fresh variant URL. A syndication metadata denial
  remains terminal because it proves the tweet itself is inaccessible from the
  current vantage point.
- **Evidence:** `ResolveVideo` and `Download` shared `statusToErr`, so both
  HTTP 403 responses became `ErrGeoRestricted`. `DownloadAndStage` converted
  that error into a nil-error terminal reject, bypassing the workflow's four
  transient download attempts. The archived Python implementation made the
  same conflation even though its CDN message admitted that authentication,
  rather than geography, could be the cause.
- **Implemented locally; not deployed:** `Download` now returns the distinct
  transient `ErrCDNForbidden` without logging the signed variant URL.
  `DownloadAndStage` propagates it as an activity error. Each Temporal retry
  reruns `ResolveVideo` before downloading, so it can obtain a refreshed
  variant URL and makes four total attempts before FF-002 records a correlated
  `download_error`. Resolve-time HTTP 403 still becomes the terminal
  `geo_restricted` outcome.
- **Regression:** Adapter tests distinguish metadata 403 from CDN 403;
  activity tests prove the split between terminal and retryable outcomes; the
  workflow test requires four attempts for `ErrCDNForbidden`. Focused uncached
  tests pass.
- **Decision:** [CDN download denial is transient](./decisions/2026-08-17-cdn-download-denial-is-transient.md).
- **Source relation:** Closes `AUD-0813-P2-3` and narrows, rather than removes,
  the historical non-retryable geo-restriction contract.

### FF-012 — permanent LLM failures consume transient retries

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Audit 2026-08-15, revalidated against `llm.classifyError`,
  `vision.ValidateClip`, and the EventWorkflow vision activity policy.
- **Invariant:** Only a failure that can change without code or configuration
  changes may consume the vision activity's retry budget.
- **Evidence:** The LLM adapter already typed HTTP 400, 401/403, and 404 as
  permanent sentinels, but `ValidateClip` returned every model error as an
  ordinary retryable Go error. It also returned malformed structured content
  as an untyped JSON error. EventWorkflow consequently ran all three activity
  attempts for invalid requests, bad credentials, missing models, and malformed
  model responses.
- **Implemented locally; not deployed:** The adapter now types invalid JSON in
  a successful OpenAI-compatible wire response as `ErrInvalidJSON`.
  `ValidateClip` converts that class, malformed structured content,
  `ErrModelNotFound`, `ErrInvalidRequest`, and `ErrAuthFailed` into a
  non-retryable Temporal ApplicationError while preserving the sentinel cause.
  Rate limit, capacity, unavailable, and unclassified infrastructure errors
  remain retryable. A failed validation still records `vision_error` and
  reclaims staging through the existing pipeline callback.
- **Regression:** Adapter tests cover invalid 2xx JSON. Activity table tests
  require every permanent sentinel and malformed model content to become a
  non-retryable ApplicationError while rate limiting remains retryable.
  Workflow tests require one attempt for permanent failure and three for
  transient failure. Focused uncached tests pass.
- **Source relation:** Implements the non-retryable model-response contract
  already specified in the [rebuild plan](./design/rebuild-plan.md#4-domain-model);
  no architectural divergence.

### FF-030 — complete rank ties depend on database row order

- **Status:** `implemented`; not deployed
- **Severity:** P3
- **Source:** Audit 2026-08-13 P3-15, revalidated against `RebalanceRanks` and
  `video.CompareShares`.
- **Invariant:** Rebalancing the same active shares with unchanged ranking
  evidence must produce the same order regardless of database row-return order.
- **Evidence:** `RebalanceRanks` intentionally reads active shares without an
  `ORDER BY`, then uses a stable in-memory sort. `CompareShares` returned equal
  when verification, popularity, size, and `created_at` all tied, leaving that
  stable sort dependent on PostgreSQL's unspecified input order.
- **Implemented locally; not deployed:** Public share ID is the final lexical
  tiebreaker after `created_at`. It changes only a complete tie and gives the
  comparator a total order while preserving every established ranking rule.
- **Regression:** Domain tests require both comparison directions and retain
  equality only when comparing the same share. Focused uncached tests pass.
- **Source relation:** Closes `AUD-0813-P3-15`. It extends the rebuild plan's
  final `created_at` rule only for values the plan left indistinguishable.

### FF-031 — missing API minute rejects clock-bearing soccer clips

- **Status:** `implemented`; not deployed
- **Severity:** P3
- **Source:** Audit 2026-08-13 P3-1, revalidated against the Go evaluator and
  archived Python timestamp guard.
- **Invariant:** Missing provider timestamp evidence cannot prove that an
  otherwise valid soccer clip shows the wrong event.
- **Evidence:** `vision.Evaluate` derived expected minute zero when
  `Expected.Elapsed` was unset. Soccer footage with any readable broadcast
  clock then failed the minute/period comparison and was rejected. The retired
  Python `validate_timestamp` explicitly returned `unverified` when
  `api_elapsed` was zero.
- **Implemented locally; not deployed:** After the soccer and screen-recording
  gates pass, `Elapsed <= 0` now routes the clip to the unverified pool with
  reason `API minute unavailable`. The content gates still reject non-soccer
  and phone-of-TV footage; no matched minute is claimed without API evidence.
- **Regression:** Domain tests cover clock-bearing and no-clock soccer footage,
  no false matched minute, and preservation of both content gates. Focused
  uncached domain, activity, and workflow tests pass.
- **Source relation:** Closes `AUD-0813-P3-1` and restores archived Python
  behavior without changing ordinary known-minute validation.

### FF-032 — LLM concurrency test races on captured request state

- **Status:** `implemented`; not deployed
- **Severity:** P3
- **Source:** Pre-build `make test-race` on 2026-08-17.
- **Invariant:** A concurrency regression test must be race-free itself so a
  red race gate identifies product code rather than its harness.
- **Evidence:** `TestChat_ConcurrencyCap` sends concurrent requests to the mock
  OpenAI-compatible server. Each handler wrote `lastChatBody` without
  synchronization, and the full race gate reported concurrent writes at that
  assignment.
- **Implemented locally; not deployed:** The mock protects captured request
  state with an RW mutex and exposes a copy-returning accessor. Structured and
  plain request-shape assertions use the accessor instead of reading the shared
  slice directly.
- **Regression:** The uncached targeted LLM package race test passes. The full
  repository race gate is rerun as the release-candidate gate after this commit.
- **Source relation:** New test-infrastructure finding; no production behavior
  changes.

### FF-033 — stopped Firefox container can masquerade as provisioned

- **Status:** `implemented`; not deployed
- **Severity:** P2
- **Source:** Pre-build review of the per-event search-instance lifecycle on
  2026-08-17.
- **Invariant:** `ProvisionFirefox` may return an event address only after its
  Docker container is running. Browser readiness may continue asynchronously,
  but a known Docker start failure must remain an activity failure.
- **Evidence:** A successful create followed by a failed start left a stopped,
  deterministically named container. On Temporal retry, `Provision` found that
  container, called `ContainerStart`, discarded its error, and returned the
  address as success. The EventWorkflow then targeted a dead hostname and
  degraded to the shared Twitter fallback instead of retrying provisioning.
- **Implemented locally; not deployed:** Fleet inspection now carries the
  container's running state. A running instance remains an idempotent no-op; a
  stopped instance must restart successfully, and a restart error propagates
  to Temporal. Provisioning still performs no blocking browser-health wait.
- **Regression:** The in-memory daemon reproduces create-success/start-failure,
  retries the same event against the stopped container, and requires both
  attempts to return the daemon start error.
- **Source relation:** New bounded failure-state bug. It does not change #160's
  zero-warm lifecycle or FF-017's container-level browser recovery.

## Confirmed lower-priority backlog

| ID | Severity | Source | Summary | Completion condition |
|---|---|---|---|---|
| FF-008 | P2 | Audit 2026-08-15 | Worker `/scratch` has no orphan sweep after process/OOM failure. | Bounded startup or scheduled sweep with active-path exclusions and tests. |
| FF-009 | P2 | Audits 2026-08-13 P2-12 and 2026-08-15 | Temporal schedules are create-only and retention still passes a hardcoded 14 days. | Config value wired; Describe→Update reconciliation tested and documented. |
| FF-010 | P2 | Audit 2026-08-15 | Completed fixtures are not revisited for late assist backfill. | Bounded completed-fixture refresh policy with vendor-call budget and tests. |
| FF-011 | P2 | Audit 2026-08-15 | Popularity increments are not idempotent under activity retry. | Retry-safe vote accounting with an invariant test. |
| FF-013 | P2 | Audit 2026-08-15 | Schema guard verifies one fingerprint, not the existence of every object after interrupted initialization. | Verify required objects or adopt ordered migrations; test partial schema. |
| FF-024 | P2 | FF-006 follow-up; current code | The `staging/` prefix has no bounded orphan sweep after abnormal workflow or process termination. | List by prefix with an age floor, protect keys owned by active work, delete only proven orphans, and test both exclusions and bounded cleanup. |
| FF-025 | P2 | FF-007 recovery boundary | A wedged EventWorkflow can remain `RUNNING`, so failed-only Workflow ID reuse correctly refuses to replace it while the downstream row and Firefox stay pinned. | Inspect Temporal status and age, recover only executions proven stale under a conservative bound, safely terminate/re-drive them, and never bypass score or downstream-completion invariants. |

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
| `AUD-0813-P2-1` | API | Fixture reads use N+1 event/video queries and the completed bucket is unbounded at the query layer. | `triage` |
| `AUD-0813-P2-5` | workflow | Serialized selector consumer blocks on persistence I/O that may not require serialization. | `triage` |
| `AUD-0813-P2-6` | Temporal | One task queue lets LLM semaphore waiters starve I/O-bound activities. | `triage` |
| `AUD-0813-P2-7` | fleet | Firefox provisioning runs sequentially inside the 30-second active-poll path. | `triage` |
| `AUD-0813-P2-8` | discovery | Forensic candidate persistence blocks child workflow spawn on the speed-to-clip path. | `triage` |
| `AUD-0813-P2-9` | ffmpeg | Dense hashing and latency-sensitive probe/frame work share one process lane. | `triage` |
| `AUD-0813-P2-13` | observability | `calls_total{error_class}` can remain empty because emitted error fields do not populate that label. | `triage` |
| `AUD-0813-P3-2` | video | A hash shorter than the configured dedup window can pass while being structurally unable to deduplicate. | `triage` |
| `AUD-0813-P3-4` | ranking | Dedup winner selection and public ranking may use inconsistent quality metrics. | `triage` |
| `AUD-0813-P3-6` | monitor | Discovery recovery may repeat duplicate start/register work every cycle for healthy workflows. | `triage` |
| `AUD-0813-P3-7` | eventing | Coincident active/staging polls may emit fixture activation twice. | `triage` |
| `AUD-0813-P3-9` | ingest | An empty tracked-team cache may still burn lookahead API calls whose results are discarded. | `triage` |
| `AUD-0813-P3-10` | aliases | National-team resolution may take the club branch after a soft profile failure. | `triage`; may be superseded by resolver removal |
| `AUD-0813-P3-12` | discovery | `max_age_minutes` has inconsistent fallback defaults. | `triage` |
| `AUD-0813-P3-13` | dedup | Workflow-replayed perceptual matching allocates per offset and lacks a cheap prefilter. | `triage` |
| `AUD-0813-P3-14` | ranking | Full rank rebalance runs after every promote and supersede rather than once per settled batch/event. | `triage` |
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
FF-006; P2-12 maps to FF-009; P2-15 is broadened by FF-014; P3-8 closed with
P1-2 under FF-027; and P3-11 was superseded when the alias resolver was
removed. P0-1
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
