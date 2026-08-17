# Active work and issue register

This file is the canonical project backlog. It contains only current bugs,
accepted improvements, and deferred decisions or validation. Closed issue
narratives and the completed documentation-normalization register are preserved
in the [2026-08-17 release snapshot](./history/issue-register-2026-08-17.md).
Point-in-time audits under [`design/audits/`](./design/audits/) are evidence,
not parallel task lists.

The latest independent code-first review is the
[2026-08-17 Codex audit](./design/audits/audit-2026-08-17-codex.md). It
revalidated the unresolved 2026-08-13 and 2026-08-15 findings and is the
evidence source for FF-034 through FF-049.

Code remains the authority for current behavior. Before promoting an audit
claim into the confirmed backlog, reproduce it or verify the cited path against
the current branch.

## Working rules

- Work one issue at a time from **Next** unless production evidence changes
  the priority.
- Give every accepted issue a stable ID. Do not reuse IDs.
- Keep evidence concrete: fixture/event/workflow IDs, timestamps, code paths,
  or a deterministic test.
- A fix is complete only when code, regression tests, and affected as-built
  documentation land together.
- Production deployment and production data repair are separate operations;
  each still requires explicit user approval.
- Preserve completed entries in a dated history snapshot instead of leaving
  them in this active register.

### Status and severity

| Value | Meaning |
|---|---|
| `next` | The single issue selected for implementation. |
| `confirmed` | Reproduced or verified against current code; ready to schedule. |
| `triage` | Preserved from an audit but not yet re-verified against current code. |
| `mitigated` | Still present in code, with an operational guard reducing current impact. |
| `implemented` | Code, regression tests, and docs are complete locally; commit or rollout remains. |
| `closed` | Fix is committed and its production release completed successfully. |
| `blocked` | Requires a decision or external dependency before implementation. |
| `P0` | Active outage, corruption, or broad clip loss. |
| `P1` | User-visible correctness failure or material resource/lifecycle leak. |
| `P2` | Bounded failure-state bug, operability gap, or performance debt. |
| `P3` | Cleanup or improvement without a current correctness failure. |

## Deployed pending live validation

| ID | Severity | Status | Summary |
|---|---|---|---|
| FF-034 | P1 | `implemented` | Candidate evidence and terminal outcome are one durable invariant; the first post-release event remains to validate. |

## Confirmed issues

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
- **Rollout:** Design work only; this did not block the 2026-08-17 correctness
  and lifecycle release.
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
  clip, so this did not block the 2026-08-17 release.
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
  throughput fix did not block the 2026-08-17 release.
- **Source relation:** The 2026-08-13 audit identified heartbeat coverage and
  shared-semaphore contention. It did not demonstrate this post-heartbeat 4K
  failure or invalidate the full-resolution hashing cost assumption.

### FF-034 — candidate evidence and terminal outcome are not one invariant

- **Status:** `implemented`
- **Severity:** P1
- **Invariant:** An EventWorkflow may report completion only after every
  observed candidate has one durable terminal state, with the evidence needed
  to explain and recover that state.
- **Cause:** `StoreCandidate` blocks child launch but its failure is ignored;
  `RecordCandidateOutcome` is also best-effort, accepts a zero-row `UPDATE`, and
  has its error discarded. A parent can therefore complete with a missing row
  or a row still marked `pending`.
- **Design:** Introduce a workflow-owned `CandidateEvidence` contract carrying
  URL, tweet text, username, age, query/attempt, and event context. Observation
  persistence must not delay clip launch. Terminal persistence must use an
  idempotent UPSERT and complete before the parent reports success. Recovery
  must distinguish observed, in-flight, and terminal candidates explicitly.
- **Implemented:** New executions launch every candidate before awaiting its
  observation insert, then require an evidence-carrying terminal UPSERT before
  candidate ownership becomes terminal. A failed observation insert leaves the
  search attempt uncheckpointed; a failed terminal UPSERT fails the parent and
  leaves its downstream checklist open. Replacement executions re-drive
  observed rows, while terminal rows only seed search exclusions. Temporal
  change ID `ff-034-candidate-durability` preserves older histories' command
  sequence.
- **Rollout:** Commit `f70cfea` deployed successfully on 2026-08-17 at
  13:42 UTC. Both workers, the API, and Twitter reported the exact release
  identity; workers registered all schedules, Twitter was authenticated and
  healthy, and no fleet container was active or stranded. The regression test
  proves an injected terminal persistence failure cannot mark its checklist
  complete. Production still needs one natural post-release EventWorkflow to
  prove it completes with no `pending` candidate rows.
- **Existing data:** The post-release read-only check found 38 candidate rows
  still `pending` under already-completed workflows, all from before this
  release. FF-034 prevents new rows in that state; it does not rewrite
  historical evidence. Any backfill or terminal classification is a separate
  production-data mutation and requires its own design and approval.
- **Relation:** This is the durable-state half of the observed “pending after
  parent workflow” defect and the evidence boundary required for FF-003's
  semantic validation. It also absorbs `AUD-0813-P2-8`; independently
  serialized persistence remains FF-046.
- **Source:** [2026-08-17 Codex audit](./design/audits/audit-2026-08-17-codex.md#ff-034--candidate-evidence-and-terminal-state-are-not-one-invariant).

## Confirmed and mitigated backlog

| ID | Severity | Status | Summary | Completion condition |
|---|---|---|---|---|
| FF-008 | P2 | `confirmed` | Worker `/scratch` has no orphan sweep after process/OOM failure. | Bounded startup or scheduled sweep with active-path exclusions and tests. |
| FF-009 | P2 | `confirmed` | Temporal schedules are create-only and retention still passes a hardcoded 14 days. | Config value wired; Describe→Update reconciliation tested and documented. |
| FF-010 | P2 | `confirmed` | Completed fixtures are not revisited for late assist backfill. | Bounded completed-fixture refresh policy with vendor-call budget and tests. |
| FF-011 | P2 | `confirmed` | Popularity increments are not idempotent under activity retry. | Retry-safe vote accounting with an invariant test. |
| FF-013 | P2 | `confirmed` | Schema guard can accept an incomplete schema and evolution is init-file/manual-ALTER based. | Establish ordered migrations and test interrupted/partial state before new constraints. |
| FF-024 | P2 | `confirmed` | The Garage `staging/` prefix has no bounded orphan sweep after abnormal termination. | Protect active keys and delete only proven age-bounded orphans. |
| FF-035 | P2 | `implemented` | Each binary now parses only its owned typed sections and rejects semantic or cross-field violations before external work. A derived contract test keeps Go tags, `.env.example`, Compose overrides, environment scope, and cookie mounts aligned; dead config keys were removed. | Roll out the committed release and verify clean startup for worker, API, Twitter, and one VNC config parse. |
| FF-036 | P2 | `confirmed` | API completed-fixture reads are unbounded and assembled with N+1 queries. | Separate the public read window from durable URL tombstones and batch assembly. |
| FF-037 | P2 | `mitigated` | LLM, Temporal, and ffmpeg admission are process-local and share work lanes. | Dedicated task/ffmpeg lanes; checked aggregate limits; shared inference owns global admission. |
| FF-038 | P2 | `mitigated` | Firefox capacity, leases, and Docker access are not one atomic controller boundary. | HTTP fleet controller with atomic admission, scoped labels, reaping, and no worker socket. |
| FF-039 | P2 | `confirmed` | API/worker/Twitter lifecycle, readiness, metrics identity, and error classification diverge. | Shared lifecycle contract, real readiness, correct error classes, standard identity labels. |
| FF-040 | P2 | `confirmed` | Live reconciliation omits mutable fixture metadata and activation is not atomic across pollers. | Explicit ownership of mutable fields plus one atomic state transition. |
| FF-041 | P2 | `confirmed` | Perceptual hash bytes have no algorithm version or minimum viable sequence invariant. | Version hashes and reject too-short streams before FF-005 preprocessing changes. |
| FF-042 | P2 | `implemented` | Lint/tool versions, formatting, and module state were not reproducible. | Go 1.25.11, golangci-lint 2.12.2, and Air 1.65.3 are pinned; format, tidy, vet, lint, short, full, and race gates pass. |
| FF-043 | P2 | `implemented` | The public API now starts from Postgres and S3 only; NATS remains worker-owned and the BFF subscribes directly. The API profile ignores shared NATS env and Compose drops its API-specific override, while `luv-*` remains for real BFF HTTP calls. | Roll out the committed release and verify API startup plus REST health while the NATS broker is unavailable. |
| FF-044 | P3 | `confirmed` | Recovery repeats start/describe work every 30 seconds for healthy discovery workflows. | Durable next-check lease or scheduled supervisor with bounded checks. |
| FF-045 | P3 | `confirmed` | Dormant code/schema surfaces and oversized composition files obscure ownership. | Caller-proven deletion and in-package splits after related behavior fixes. |
| FF-046 | P2 | `confirmed` | Ancillary persistence blocks the serialized EventWorkflow selector consumer. | Serialize only dedup state; model durable effects with explicit futures/idempotency. |
| FF-047 | P3 | `confirmed` | Empty tracked-team state still burns fixture lookahead calls whose results are discarded. | Short-circuit before vendor fixture calls and emit degraded-state telemetry. |
| FF-048 | P2 | `confirmed` | Share minting uses check-then-insert without `(event_id, asset_id)` uniqueness. | Database constraint plus atomic idempotent insert after FF-013. |
| FF-049 | P3 | `confirmed` | Documentation routing is clean, but several current/reference documents still exceed the shared size standard. | Split the 618-line orchestration ledger and route the 2,869-line Python functional spec plus 604-line video-dedup proposal by topic without rewriting historical claims. |
| FF-050 | P2 | `investigate` | The event-to-surface critical path is not measured end to end and contains possible avoidable serial barriers. | Measure phase and queue latency under representative concurrency, then simplify or parallelize only the demonstrated bottleneck without weakening correctness or resource caps. |

### FF-050 — measure and shorten event-to-surface latency

- **Outcome:** Surface the first valid clip as soon as the required evidence is
  available. Preserve search coverage, Temporal durability, exact-byte
  ownership, serialized perceptual-dedup decisions, and the LLM/ffmpeg
  admission limits.
- **Current boundaries to measure:** Twitter returns a completed scroll batch
  instead of candidates as they appear; dense hashing completes before vision
  starts; terminal and ancillary persistence wait inside the serialized
  selector; and expensive activities share one general Temporal task lane.
- **Required work:** Add correlated phase and queue timings from provider
  observation through frontend notification. Use representative match-day
  concurrency to separate intentional waits—three-poll event debounce,
  discovery spacing, Twitter stealth pacing, exact-duplicate ownership, and
  bounded resource admission—from avoidable waits. Measure FF-034's concurrent
  observation persistence, then evaluate early candidate delivery, hash/vision
  overlap, and asynchronous durable effects only where the evidence justifies
  them.
- **Completion boundary:** Record before/after critical-path and saturation
  evidence for each accepted change. Prefer the smallest change that removes
  the measured bottleneck; do not add a streaming protocol, queue, service, or
  concurrency layer without a demonstrated benefit. FF-034 owns candidate
  durability, FF-037 owns work-lane isolation, and FF-046 owns selector-blocking
  durable effects.

## Deferred decisions and validation

These are not accepted correctness bugs. They stay visible until measurement or
product direction justifies promotion. The complete prior-audit mapping is in
the [2026-08-17 Codex audit](./design/audits/audit-2026-08-17-codex.md#prior-audit-disposition).

| Source | Decision or evidence still required |
|---|---|
| `AUD-0813-CF-153` | On the next real cookie expiry, capture VNC write-back and propagation to new fleet instances end to end. Auth expiry itself is observed and correctly surfaced. |
| `AUD-0813-CF-175` | Decide whether national-team coverage needs an explicit seed beyond league-derived rosters. |
| `AUD-0813-CF-179` | Measure public playback before restoring unused `ffmpeg.Faststart`. |
| `AUD-0813-CF-SLO` | Define a match-coverage SLO before adding summary storage or alerts. |
| `AUD-0813-CF-SCORE` | Decide whether clients need score-at-detection history. |
| `AUD-0813-P3-4` | Dedup winner quality and public ranking serve different policies; document them before changing either. |
| `AUD-0813-P3-14` | Measure rank-rebalance cost at real event sizes before replacing the simpler full rebalance. |
| `AUD-DESIGN-TRACING` | Add distributed tracing only when a concrete cross-service diagnostic requires it. |

Do not schedule global coverage floors, a generated log catalog, or speculative
Twitter rate-limit backoff. The August 15–17 log sample showed auth expiry but
no Twitter rate-limit, interstitial, HTTP 429, or browser-failure event.

## Behavior that is intentional

- Goals, red cards, and missed penalties all run the full discovery workflow,
  including the configured 15 Twitter search attempts.
- Perceptual dedup is event-scoped and category-scoped. Do not classify the
  lack of general cross-event fuzzy dedup as a bug without new evidence.
- The archived Python implementation is a behavioral reference, not the Go
  architecture template.
