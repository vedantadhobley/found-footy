# Active work and issue register

This file is the canonical project backlog. It contains only current bugs,
accepted improvements, and deferred decisions or validation. Closed issue
evidence is routed through the [history index](./history/README.md); the
release-day register remains preserved as a frozen snapshot.
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
| `validating` | Code and production rollout are complete; a natural workload still owes the stated proof. |
| `closed` | Fix is committed and its production release completed successfully. |
| `blocked` | Requires a decision or external dependency before implementation. |
| `P0` | Active outage, corruption, or broad clip loss. |
| `P1` | User-visible correctness failure or material resource/lifecycle leak. |
| `P2` | Bounded failure-state bug, operability gap, or performance debt. |
| `P3` | Cleanup or improvement without a current correctness failure. |

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

- **Status:** `validating`
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
- **Implemented:** ffmpeg now samples, converts to grayscale, and area-reduces
  each hashing frame to 640 pixels wide before lossless PNG serialization. Go
  still performs histogram equalization and the final 9×8 area reduction, so
  the dHash definition remains grayscale → equalize → 9×8 → adjacent
  differences. `hash_version` persists the algorithm, preprocessing, and sample
  interval; only identical versions compare. Existing and rolling-old-binary
  rows become `dhash-v1-unversioned`. New 0.1-second sequences use
  `dhash-v2-gray640-equalized-area9x8@0.1s`. A sequence shorter than the
  configured 30-frame match window returns terminal
  `insufficient_hash_frames`; byte-identical waiters share that verdict instead
  of repeating deterministic work.
- **Bounded benchmark:** On a synthetic five-second 3808×2146 source at 10 fps
  under four CPUs and 2 GiB, the old ffmpeg stage emitted 40,075 KiB in 1.549
  seconds with 1,259,812 KiB max RSS. The bounded stage emitted 779 KiB in
  0.457 seconds with 139,128 KiB max RSS: 51× less pipe data, 3.4× lower wall
  time, and 9× lower ffmpeg RSS. This isolates preprocessing; the Huijsen source
  remains the required production proof.
- **Rollout:** Migration and release `201cdf1` completed successfully at
  03:11 UTC on 2026-08-18. The new worker/API processes accepted the schema;
  both workers, the API, and Twitter exposed the exact release identity. A 4K
  source or equivalent natural candidate still owes the production latency
  proof. Thresholds and category scoping are unchanged. Re-hash the FF-004
  pair under v2 before considering any matcher change.
- **Source relation:** The 2026-08-13 audit identified heartbeat coverage and
  shared-semaphore contention. It did not demonstrate this post-heartbeat 4K
  failure or invalidate the full-resolution hashing cost assumption.

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
| FF-042 | P2 | `implemented` | Lint/tool versions, formatting, and module state were not reproducible. | Go 1.25.11, golangci-lint 2.12.2, and Air 1.65.3 are pinned; format, tidy, vet, lint, short, full, and race gates pass. |
| FF-043 | P2 | `implemented` | The public API now starts from Postgres and S3 only; NATS remains worker-owned and the BFF subscribes directly. The API profile ignores shared NATS env and Compose drops its API-specific override, while `luv-*` remains for real BFF HTTP calls. | Roll out the committed release and verify API startup plus REST health while the NATS broker is unavailable. |
| FF-044 | P3 | `confirmed` | Recovery repeats start/describe work every 30 seconds for healthy discovery workflows. | Durable next-check lease or scheduled supervisor with bounded checks. |
| FF-045 | P3 | `confirmed` | Dormant code/schema surfaces and oversized composition files obscure ownership. | Caller-proven deletion and in-package splits after related behavior fixes. |
| FF-046 | P2 | `confirmed` | Ancillary persistence blocks the serialized EventWorkflow selector consumer. | Serialize only dedup state; model durable effects with explicit futures/idempotency. |
| FF-047 | P3 | `confirmed` | Empty tracked-team state still burns fixture lookahead calls whose results are discarded. | Short-circuit before vendor fixture calls and emit degraded-state telemetry. |
| FF-048 | P2 | `confirmed` | Share minting uses check-then-insert without `(event_id, asset_id)` uniqueness. | Database constraint plus atomic idempotent insert after FF-013. |
| FF-049 | P3 | `confirmed` | Documentation routing is clean, but several current/reference documents still exceed the shared size standard. | Split the 618-line orchestration ledger and route the 2,869-line Python functional spec plus 604-line video-dedup proposal by topic without rewriting historical claims. |
| FF-050 | P2 | `investigate` | Live Elche timing shows 23.6 seconds from valid-candidate observation to publication, dominated by 12.6-second hashing and 9.7-second vision; durable effects add milliseconds. | Measure the deployed bounded-hash latency before considering pipeline concurrency. |
| FF-052 | P1 | `confirmed` | Vision accepted a phone filming a display as a clean Elche broadcast with `screen=false` on all three sampled frames. | Preserve the clip as a regression sample, calibrate the prompt/model against varied display recordings, and prove rejection without increasing clean-broadcast false positives. |
| FF-053 | P1 | `validating` | The 1.75 minimum aspect gate discarded four 1.739 Elche candidates before download even though at least three contained legitimate goal footage; the minimum is now 1.73 and deployed in `201cdf1`. | Prove a natural 1.73–1.749 candidate reaches download while the known ≤1.72 letterbox band remains rejected. |

### FF-053 — legitimate 1.739 landscape clips failed the metadata gate

- **Observed:** Elche's 76′ goal in Deportivo La Coruna–Elche, fixture
  `1570337`, event `a80e663d-178a-4b65-99f5-734f724ccf67`, on 2026-08-17.
- **Evidence:** Four candidates from `imov_31`, `FoudeLiga`, `ci3xii`, and
  `tikitakafut_` were rejected as `aspect_too_narrow_1.739`. Manual review
  confirmed legitimate goal footage in at least three. Because `PreFilter`
  rejects from syndication metadata, none reached download, dHash, or vision.
- **Decision:** Admit 1.73–1.82. The lower boundary sits below the observed
  legitimate 1.739 cluster but above the prior 1.60–1.72 letterbox/social
  band. This changes admission only; dHash Hamming and run thresholds remain
  unchanged. See the [decision record](./decisions/2026-08-17-live-evidence-sets-landscape-aspect-admission.md).
- **Verification:** Domain tests pin 1.739 and exactly 1.730 as accepted,
  1.729 as rejected, and 16:10 as rejected in both pre- and post-download
  gates. Release `201cdf1` deployed successfully at 03:11 UTC on 2026-08-18;
  the next natural candidate in the changed band still owes live proof.

### FF-052 — phone-of-display clip passed vision validation

- **Observed:** The sole surfaced asset for the same Elche event came from
  the [XimoSantanaaa source tweet](https://x.com/XimoSantanaaa/status/2089451963997933625)
  and is a phone recording of a display.
- **Evidence:** The durable Temporal result classified all three frames as
  `soccer=true, screen=false` (`ScreenVotes=0`) and read clocks `42:28`,
  `75:39`, and `75:42`; two clocks supported the 76′ event, so the candidate
  was promoted as verified. The intended majority screen gate exists and
  works when the model supplies positive votes; this is a classifier false
  negative, not a missing workflow branch.
- **Constraint:** Do not remove or bypass vision, and do not reject all
  fan-shot footage. Calibrate with positive phone/TV examples plus clean
  broadcasts so a tighter rubric does not trade this false accept for broad
  clip loss.

### FF-050 — measure and shorten event-to-surface latency

- **Outcome:** Surface the first valid clip as soon as the required evidence is
  available. Preserve search coverage, Temporal durability, exact-byte
  ownership, serialized perceptual-dedup decisions, and the LLM/ffmpeg
  admission limits.
- **Live evidence:** Elche event
  `a80e663d-178a-4b65-99f5-734f724ccf67` began 60.0 seconds after first
  provider observation, consistent with the three-poll debounce. The first
  candidates arrived on search attempt 2; the eventual survivor arrived on
  attempt 3 at event elapsed 126.635 seconds. From that observation to frontend
  publication took 23.638 seconds: download 1.392 seconds, dense hash 12.575
  seconds, vision 9.652 seconds, and promotion plus publication 45 milliseconds.
  Observation and terminal persistence added only tens of milliseconds.
- **Interpretation:** The post-discovery path does not contain an unexplained
  queue or persistence pause in this sample. Hashing and vision are the
  critical path. Running them concurrently could save at most the shorter
  stage. Category-scoped perceptual dedup already runs after vision, so every
  hash-successful MD5-unique clip receives the model call today; overlap would
  add vision work only for clips whose hash later fails or returns an
  insufficient sequence. It would still couple the two shared ffmpeg lanes and
  require a durable per-candidate join/cancellation policy. FF-041/FF-005 now
  bound and version hashing locally; measure the deployed result before
  reconsidering overlap.
- **Rollout:** Commit `0e1bbdf` deployed successfully on 2026-08-17 at
  14:10 UTC. Both workers, the API, and Twitter reported the exact release
  identity; all schedules were active, Twitter was authenticated and healthy,
  and no scoped fleet instance was running or stranded. The Elche workflow is
  the first representative production measurement after that rollout.
  Bounded hashing then deployed in release `201cdf1` at 03:11 UTC on
  2026-08-18. Immediate health and schema checks passed; no active fixture was
  available for the before/after candidate-path measurement.
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
