# Active work and issue register

This file is the canonical project backlog. It contains only current bugs,
deferred work, and audit findings that still require validation. Closed issue
narratives and the completed documentation-normalization register are preserved
in the [2026-08-17 release snapshot](./history/issue-register-2026-08-17.md).
Point-in-time audits under [`design/audits/`](./design/audits/) are evidence,
not parallel task lists.

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

## Next

| ID | Severity | Status | Summary |
|---|---|---|---|
| — | — | — | No issue selected after the 2026-08-17 production release. |

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

## Confirmed lower-priority backlog

| ID | Severity | Source | Summary | Completion condition |
|---|---|---|---|---|
| FF-008 | P2 | Audit 2026-08-15 | Worker `/scratch` has no orphan sweep after process/OOM failure. | Bounded startup or scheduled sweep with active-path exclusions and tests. |
| FF-009 | P2 | Audits 2026-08-13 P2-12 and 2026-08-15 | Temporal schedules are create-only and retention still passes a hardcoded 14 days. | Config value wired; Describe→Update reconciliation tested and documented. |
| FF-010 | P2 | Audit 2026-08-15 | Completed fixtures are not revisited for late assist backfill. | Bounded completed-fixture refresh policy with vendor-call budget and tests. |
| FF-011 | P2 | Audit 2026-08-15 | Popularity increments are not idempotent under activity retry. | Retry-safe vote accounting with an invariant test. |
| FF-013 | P2 | Audit 2026-08-15 | Schema guard verifies one fingerprint, not the existence of every object after interrupted initialization. | Verify required objects or adopt ordered migrations; test partial schema. |
| FF-024 | P2 | FF-006 follow-up; current code | The `staging/` prefix has no bounded orphan sweep after abnormal workflow or process termination. | List by prefix with an age floor, protect keys owned by active work, delete only proven orphans, and test both exclusions and bounded cleanup. |

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

The later audits own the disposition of earlier findings. Do not intake the
2026-08-05 audit independently. The complete closed, duplicate, superseded,
and intentional mappings through the release are preserved in the
[2026-08-17 snapshot](./history/issue-register-2026-08-17.md#audit-intake-requiring-current-code-validation).
Revalidate a closed mapping only when new production evidence contradicts it.

## Behavior that is intentional

- Goals, red cards, and missed penalties all run the full discovery workflow,
  including the configured 15 Twitter search attempts.
- Perceptual dedup is event-scoped and category-scoped. Do not classify the
  lack of general cross-event fuzzy dedup as a bug without new evidence.
- The archived Python implementation is a behavioral reference, not the Go
  architecture template.
