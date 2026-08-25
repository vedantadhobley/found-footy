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

### FF-003 — candidate can pass without exact-event semantic evidence

- **Status:** `confirmed`
- **Severity:** P1
- **Observed:** Lens–PSG fixture `1546791` on 2026-08-16,
  Barcelona–Al Ahly fixture `1604797` on 2026-08-19, and Atletico
  Madrid–Malaga fixture `1570334` on 2026-08-19.
- **Invariant:** A candidate should not surface as footage of an exact event
  solely because a broad team/player term, generic football imagery, or a
  matching scorebug clock passed independently. Stronger evidence that the
  media depicts another event, an edited substitution, or a non-match meme
  must prevent exact-event attribution. The same genuine clip may legitimately
  represent multiple events; cross-event reuse is not itself a defect.
- **Cross-event evidence:** Tweet
  `https://x.com/FH4A/status/2089071008082784644`
  (`md5=059e019aafd963d208782d35e8d1eb12`, 11.9 seconds) was promoted as
  unverified for both Thauvin's 32′ goal and Antonio's 39′ red card. Its Arabic
  text explicitly describes Antonio's red card. It appeared immediately after
  that event and Antonio's `(antonio OR Lens)` search found it on attempt 1.
  Thauvin's older `(thauvin OR Lens)` workflow found it on attempt 8 through
  the generic `#lens` match. With no readable broadcast clock, both workflows
  accepted it as unverified.
- **Edited-event evidence:** Gordon's missed-penalty event
  `51fcd0ba-fe31-4164-bd7d-676149341652` promoted tweet
  `https://x.com/ThomX77/status/2090170818001018909` as share
  `s_531b03a142e9`. It is rank 1, clock-verified at 90′, and has popularity 20.
  User visual review found that the clip is a meme which edits Gabriel's
  Champions League penalty miss over Gordon's miss. The clock gate and generic
  football gate therefore behaved as specified, but the sampled evidence did
  not establish that the depicted kick was Gordon's event.
- **Non-event meme evidence:** Lee Kang-in's 70′ goal event
  `8f04ffe7-9207-4727-b093-98cf3013c6f4` promoted tweet
  `https://x.com/neferlipa/status/2090176941257044133` as share
  `s_1a6954f47c3e`. It is rank 2, unverified, and has popularity 7. Its text
  strongly names Kang-in, but user visual review found a K-pop artist dancing
  in an Atletico shirt rather than goal footage. Rank 1 remains the legitimate
  clock-verified goal, so this is a secondary-result precision failure rather
  than total event coverage loss.
- **Cause:** Search intentionally permits team-only matches for recall. The
  video validator receives frames plus the expected minute, but not the stored
  tweet text, and it classifies football/screen/clock rather than semantic
  relevance or authenticity for the event's player and action. Independent
  per-event workflows therefore have no evidence-resolution step when the
  clock is absent. A matching clock is also only timestamp evidence: the fixed
  three-frame sample can validate a genuine wrapper while missing an inserted
  scene, and the pipeline has no edit/scene-consistency check.
- **Constraints:** Keep event-scoped perceptual dedup; fixture-scoped exact or
  fuzzy uniqueness would incorrectly reject a goal-plus-card sequence,
  compilation, or another genuine multi-event clip. Do not implement
  first-claim-wins ownership. Do not hard-reject on meme keywords or tweet text
  alone; ordinary football posts use jokes, edits, and unrelated references.
- **Design directions:** Evaluate search-query precision separately from
  downstream validation. Pass tweet text and event context alongside video
  frames to multimodal validation; combine explicit player/event-type mentions,
  clock evidence, tweet-time proximity, scorebug/team evidence, and visual
  action semantics. Investigate scene-change-aware time coverage so one
  verified wrapper frame cannot authenticate an unsampled edit. Keep
  `timestamp_verified` scoped to its literal meaning; a matched clock must not
  imply exact-event authenticity or dominate ranking without a separate
  semantic result. Multiple event assignments must remain valid when evidence
  supports them; ambiguous candidates need an explicit confidence/fallback
  policy rather than forced exclusive ownership. Preserve the Lens, Gordon,
  and Lee Kang-in assets as three distinct regression classes.
- **Rollout:** Design work only; this did not block the 2026-08-17 correctness
  and lifecycle release.
- **Source relation:** Cross-event dedup was discussed and rejected on
  2026-07-25. The 2026-08-17 audit preserves FF-003's architectural evidence;
  the two 2026-08-19 meme samples expand its scope from no-clock attribution
  to exact-event authenticity, including a candidate with a matched clock.

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
  timing drift is not the primary failure.
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
| FF-045 | P3 | `implemented` | Zero-caller packages, Temporal signaling, telemetry vocabulary, and stale comments are removed. `cmd/worker` is thin; worker composition lives in `internal/app/worker`; shared discovery payloads live in `internal/contract/discovery`; event, activity, Twitter-search, Postgres, and large test files are split by responsibility without changing package or Temporal identities. | Fast and full Docker gates passed; deployed and release-identity verified in `e2e181a`. |
| FF-046 | P2 | `confirmed` | Ancillary persistence blocks the serialized EventWorkflow selector consumer. | Serialize only dedup state; model durable effects with explicit futures/idempotency. |
| FF-047 | P3 | `confirmed` | Empty tracked-team state still burns fixture lookahead calls whose results are discarded. | Short-circuit before vendor fixture calls and emit degraded-state telemetry. |
| FF-048 | P2 | `confirmed` | Share minting uses check-then-insert without `(event_id, asset_id)` uniqueness. | Database constraint plus atomic idempotent insert after FF-013. |
| FF-049 | P3 | `implemented` | The orchestration ledger, Python functional reference, and historical video-dedup proposal are split into routed topic sets whose leaves stay within the shared size standard. The EventWorkflow ledger now also makes the dedup-keeper versus public-ranking boundary explicit, resolving `AUD-0813-P3-4`. | Link checks passed and the normalization shipped in `e2e181a`; no separate runtime rollout applies. |
| FF-050 | P2 | `investigate` | Live Elche timing shows 23.6 seconds from valid-candidate observation to publication, dominated by 12.6-second hashing and 9.7-second vision; durable effects add milliseconds. | Measure the deployed bounded-hash latency before considering pipeline concurrency. |
| FF-052 | P1 | `confirmed` | Vision accepted a phone filming a display as a clean Elche broadcast with `screen=false` on all three sampled frames. | Preserve the clip as a regression sample, calibrate the prompt/model against varied display recordings, and prove rejection without increasing clean-broadcast false positives. |
| FF-053 | P1 | `validating` | The 1.75 minimum aspect gate discarded four 1.739 Elche candidates before download even though at least three contained legitimate goal footage; the minimum is now 1.73 and deployed in `201cdf1`. | Prove a natural 1.73–1.749 candidate reaches download while the known ≤1.72 letterbox band remains rejected. |
| FF-054 | P3 | `confirmed` | Zero-caller webhook tables and the outbox cursor remain in the flat schema and durable databases. Removing them during FF-045 would create a second schema-hash migration boundary while FF-041 is still converging. | After durable environments converge on FF-041, drop the three tables through one explicit in-place migration, refresh stale schema comments, update `schema.sql` and its contract test, then flatten the migration file. |
| FF-055 | P1 | `validating` | API-Football winner flags describe the live leader; nil-guarded updates retained an earlier leader when a match returned to a tie, so completed draws could expose the wrong winner. Score-derived state is deployed and the ten stale draws are repaired. | Verify a natural lead-to-tie update clears both winners through the production API and frontend. |
| FF-056 | P1 | `validating` | The Go vision port computed `elapsed + extra - 1` but then clamped normal-time results back to `elapsed`, shifting the intended ±1 clock window one minute late. Abdelkarim's API-30' goal therefore rejected genuine clips whose sampled clock read 28'. The unclamped normalization is deployed in `136e2d2`. | Verify a natural API-minus-two sampled buildup frame enters the verified pool without admitting an outside-tolerance API-minus-three frame. |
| FF-057 | P1 | `validating` | Period-aware reset clocks and exact `45:xx 2H` / `15:xx ET2` boundary alternatives are deployed. The corrected historical replay completed all 104 Barcelona–Al Ahly clock rejects across four events, normalized the 31 malformed audit envelopes from the interrupted first run, closed all four checklists, and left zero pending rows. | Verify the next natural reset-clock goal; the deterministic repair and its production exercise are complete. |
| FF-058 | P1 | `validating` | The zero-warm Firefox model had no fixture-independent cookie maintenance or DOM canary. A six-hour Temporal schedule now forces auth verification and cookie persistence on the static fallback, then requires live-search feed, video-selector, and status-URL evidence. Cookie expiry-only changes and backup/reload failures are observable. Release `e2143ac` is deployed and its unpaused schedule is registered. | Observe the first scheduled maintenance execution and one later natural success. |
| FF-059 | P1 | `implemented` | VNC now uses a separate raw Firefox ESR image and read-only profile-capture service; Playwright remains headless and search-only. Invalid or expired profiles cannot overwrite the shared backup. The immutable production VNC image built successfully in release `e2143ac`, but the optional service was not running and was not recreated. | Prove raw login → atomic capture → static `/auth/verify` → fresh fleet-instance reload in dev, then repeat production recovery only on a real authorized expiry. |
| FF-060 | P2 | `confirmed` | In the 2026-08-19 two-fixture sample, 69 of 742 candidates ended as undifferentiated `download_error`; the durable row cannot distinguish syndication lookup, missing media, CDN response, expiry, or staging failure after ephemeral logs disappear. | Persist a bounded download failure class and stage at the terminal candidate boundary; validate distributions before changing retry or filtering policy. |
| FF-061 | P1 | `validating` | `feed_timeout` laundered an unavailable Twitter feed into HTTP success and consumed one of the event's 15 search attempts. The browser/worker contract now classifies five bounded states, retains secret-free timeline evidence, and separates usable attempts from a durable bounded outage budget. Release `e2143ac` is deployed. | Verify one rendered search and one maintenance run report the new state/evidence; use the next natural burst to determine whether X exposes 429/rate headers, an error interstitial, or only unknown timeouts. See the [incident evidence](./incidents/2026-08-20-twitter-feed-suppression.md). |
| FF-062 | P1 | `validating` | A real goal that returned after reaching a removed tombstone was mapped back to that terminal row and skipped while its identity stayed exact. Leipzig fixture `1550681` initially retained five active goals against API-Football's coherent 0–6 result. Before release `e2143ac`, a provider clock correction from 45+2 to 45+1 made the return non-exact, so old code allocated generation 2 and completed the fixture. | Prove one natural exact-identity post-removal reappearance receives a new UUID and completes its own debounce/downstream lifecycle under `e2143ac`. |
| FF-063 | P1 | `confirmed` | A played terminal fixture whose provider event inventory remains permanently inconsistent has no bounded recovery state. Zaragoza–Athletic fixture `1607295` remained `active/FT`, 3–1, with zero stored events and completion counter zero three days after kickoff; both API-Football fixture and dedicated event endpoints return zero events. | Move age-bounded incoherent terminal fixtures out of the 30-second active loop into an explicit unresolved state with slow reconciliation and an auditable repair path, preserving FF-014's no-fabrication invariant. |
| FF-064 | P3 | `implemented` | Production now uses Control's canonical `control-joi.luv` inference identity with `gemma-4-12b` pinned. Both worker replicas were recreated on unchanged release `e2143aca12f90c0891d3afb6b417e29102cba710`; startup dependencies, route initialization, release metrics, scheduler work, the model catalog, and an exact Gemma sentinel passed with zero restarts. | Control retains `joi.luv` as a rollback route until observed legacy use reaches zero; no Found Footy work remains. |

### FF-062 — removed event reappearance was swallowed by its tombstone

- **Observed:** In production fixture `1550681`, Baku's goal appeared, was
  absent long enough for its first generation to reach
  `removed_reason='var'`, and then returned in API-Football's score-coherent
  six-goal inventory. The fixture showed 0–6 and three coherent terminal
  provider votes, but Postgres held only five surviving goals. The independent
  durable completion gate therefore kept the fixture active.
- **Cause:** FF-027 correctly retained removed sequences to prevent key reuse,
  but it also matched exact returned evidence to the terminal tombstone.
  Reconcile skipped that key. Temporal durability was not the failure: no new
  event UUID or EventWorkflow was created for Temporal to run.
- **Implemented:** Provider evidence now matches only active rows. Removed rows
  still contribute to the historical maximum, so a reappearance allocates the
  next sequence and a new UUID. It enters the ordinary count-1→3 debounce and
  launches a fresh downstream workflow. The old row, removal audit, revoked
  shares, deleted objects, and Temporal history remain immutable.
- **Regression:** A focused monitor test reproduces the Baku lifecycle and
  proves the new generation triggers after three observations. The scenario
  corpus runs the same appeared→removed→reappeared trace through real activity
  code and Postgres, retaining both generations with distinct natural keys.
- **Decision:** [Removed event reappearance starts a new generation](./decisions/2026-08-24-removed-event-reappearance-starts-new-generation.md).
- **Rollout:** Release `e2143ac` deployed successfully on 2026-08-24 at
  08:30 UTC. Both workers, the API, and static Twitter verified the exact
  immutable release identity. No schema or object-storage mutation was needed.
- **Pre-release recovery:** Production validation found that fixture `1550681`
  had already completed at 04:20 UTC. API-Football changed Baku's returned clock
  from the removed generation's 45+2 to 45+1; FF-027's old matcher therefore
  treated it as non-exact, allocated `_goal_2`, ran its complete discovery
  workflow, and completed the fixture. This proves a removed event can return,
  but it does not naturally exercise FF-062's corrected exact-match branch.
- **Remaining proof:** A natural exact-identity reappearance under `e2143ac`.
  Existing completed or provider-pruned fixtures need an explicit repair path;
  this change does not synthesize missing provider evidence.

### FF-063 — terminal fixture can remain active on a permanently incomplete inventory

- **Observed:** The 2026-08-24 post-release report found Zaragoza–Athletic
  fixture `1607295` still `active` with API status `FT`, score 3–1,
  `completion_counter=0`, zero stored events, and a current `last_polled_at`.
  Kickoff was 2026-08-21, so ordinary polling had not repaired it after three
  days.
- **Correct behavior retained:** FF-014 must not complete a played fixture whose
  scoring events do not explain its score. Fabricating four unknown goals or
  treating terminal status as sufficient would restore the original silent-data
  loss bug.
- **Recovery gap:** The fail-closed state has no age-bounded escalation,
  historical-event refetch, explicit unresolved terminal state, or operator
  repair contract. A provider that never returns a coherent event array leaves
  the fixture active indefinitely.
- **Provider boundary:** On 2026-08-24, the exact production
  `/fixtures?ids=1607295` request returned one valid `FT` fixture, score 3–1,
  and `events=[]`. The dedicated `/fixtures/events?fixture=1607295` endpoint
  independently returned zero events. The adapter and parser did not discard
  the inventory; API-Football does not provide it for this match.
- **Required design:** After a bounded terminal grace period, move the fixture
  out of the 30-second active loop into an explicit unresolved terminal state.
  Retry it on a slower reconciliation schedule and retain an auditable repair
  path if another source later supplies the events. Do not fabricate four
  unknown goals or mark the ordinary completed state with unexplained score.

### FF-061 — unavailable Twitter responses consumed usable searches

- **Implemented:** `internal/contract/twittersearch` is the single request,
  response, candidate, state, and evidence contract shared by the browser
  service and worker. Only `rendered` and `explicit_empty` are usable;
  `login`, `upstream_error`, and `unknown_timeout` preserve the logical
  attempt.
- **Evidence:** The browser installs SearchTimeline response/failure listeners
  before navigation, then retains the query-free final route, bounded title,
  app-shell/empty/error selector bits, response status/failure, and
  `x-rate-limit-*` values. Bodies, request headers, cookies, and tokens are
  excluded.
- **Workflow:** New histories use one SearchTweets activity call per probe and
  one-minute workflow-level spacing. The usable and unavailable budgets both
  default to 15. Both counters plus the latest evidence are monotonic
  `event_downstream_workflows.metadata` state and restore on failed-run
  recovery. Classified service failures remain retryable Temporal application
  errors with typed details: new histories decode them after one call, while
  pre-FF-061 histories keep the prior retry/checkpoint commands. Exhaustion
  closes normally as `twitter_unavailable` when no candidate ran.
- **Verification:** Unit and workflow tests cover all result classes, explicit
  empty accounting, structured non-2xx propagation, activity errors, outage
  exhaustion, rolling untyped `feed_timeout` compatibility, recovery, evidence
  redaction/bounds, metric-label bounds, and replay. The Postgres integration
  test round-trips counters/evidence and prevents an older checkpoint from
  replacing the latest observation.
- **Rollout:** Release `e2143ac` deployed successfully on 2026-08-24 at
  08:30 UTC. Both workers, the API, and static Twitter verified the exact
  immutable release identity.
- **Remaining proof:** One rendered search, one maintenance run, and a natural
  suppression window. Do not add a global limiter until measured timeline
  evidence establishes the account/IP policy.

### FF-058 — fixture-independent Twitter maintenance was not implemented

- **Observed:** The signed per-event scaling decision explicitly requires a
  timer outside fixture polling because a quiet week creates no event browser
  traffic. The Go worker registered only ingest, active-poll, and staging-poll
  schedules. The static fallback performed one startup verification, then
  could remain idle until an event browser failed over to it.
- **Failure mode:** A server-side session expiry, selector change, cookie-file
  permission failure, or expiry-only cookie refresh could remain undiscovered
  until a live event. `/auth/verify` also called the warm-path-aware method, so
  its documented “forced” verification could return without contacting X.
- **Implemented:** `TwitterMaintenanceWorkflow` runs at minute 17 every six
  hours against the already-running static fallback. It forces a live
  verification, requires cookie sync to succeed, then runs
  `football goal filter:videos` with a 24-hour local age window and requires
  rendered articles, parsed tweets, video evidence, and valid status URLs.
  The activity has one attempt; a failure remains visible instead of producing
  a retry burst. No dynamic event Firefox is provisioned.
- **Cookie/search hardening in the same boundary:** fingerprints now include
  expiry, path, and flags; cookie-domain filtering is exact; backup and reload
  failures surface in `/status` and transition audit logs; inconclusive
  network/DOM verification becomes `degraded`, not a false auth-expiry alert;
  feed-timeout searches still sync cookies; promoted entries cannot trigger
  the chronological age stop; equal jitter bounds cannot panic; cancellation
  interrupts jitter waits; tweet text truncates by Unicode code point.
- **Recovery boundary:** This maintains and detects a valid session. FF-059's
  raw login terminal owns new credential capture after full expiry.
- **Rollout:** Release `e2143ac` deployed successfully on 2026-08-24 at
  08:30 UTC. Both workers, the API, and static Twitter verified the exact
  immutable release identity. The unpaused `twitter-maintenance-scheduled`
  schedule exists with its first execution due at the next configured window.
  Static Twitter was healthy after successful startup cookie reload, auth
  verification, and backup. Canary execution remains separate evidence.

### FF-059 — VNC recovery uses the login path X already rejected

- **Evidence:** The locked
  [2026-07-22 decision](./decisions/archive-through-2026-08-16.md#2026-07-22--playwright-login-validation-twitter-blocks-playwright-login-raw-firefox-subprocess-fallback-confirmed-required)
  records X rejecting the username step in Playwright-instrumented Firefox and
  requires raw Firefox for login. Compose, the Twitter Dockerfile, entrypoint,
  and `cmd/twitter` instead ran the same Playwright browser with
  `headless=false` in VNC mode.
- **Prior impact:** Existing cookies could scrape and be maintained, but a full
  expiry could still require an out-of-band cookie import. The documented VNC
  procedure was not an evidence-backed recovery contract.
- **Implemented:** The search image is now headless Playwright only. VNC builds
  from `docker/twitter-auth/Dockerfile`, runs raw Debian Firefox ESR, and uses a
  two-second read-only SQLite capture loop. Firefox holds an exclusive database
  lock while open, so the operator closes it after login; the lock is an
  expected waiting state and capture follows the graceful close. The loop
  requires a non-expired `auth_token`, filters expired cookies, preserves the
  full cookie shape, and publishes through the existing strict-domain atomic
  writer. `/health` and `/status` expose capture evidence and build identity
  without cookie values. Invalid or unreadable profiles leave the prior backup
  untouched.
- **Local proof:** The raw image builds at 358 MB versus 1.03 GB for the
  existing Playwright search image. A disposable network-isolated container
  with no mounts proved the open-Firefox lock state, the post-close empty-
  profile state, and end-to-end conversion of synthetic Firefox rows into a
  mode-0600 backup plus `state=ready`. The container and fake profile were
  removed after the test. This does not claim a live X login.
- **Lifecycle:** The container-local supervisor owns only its raw Firefox and
  capture service. Raw login and automated search remain different containers;
  environment/network ownership continues to authorize search-fleet lifecycle.
- **Rollout boundary:** The immutable raw-Firefox VNC image built successfully
  as part of release `e2143ac`. The optional production VNC service was not
  running, so the release correctly did not create or recreate it.
- **Proof gate:** From a deliberately logged-out dev profile, authenticate in
  raw Firefox, require atomic capture, force static `/auth/verify`, then prove a
  fresh headless instance reloads and searches without copying the profile.
  Production proof requires separate authorization and a real expiry.

### FF-056 — normal-time clock normalization was cancelled by a clamp

- **Observed:** Abdelkarim's Barcelona–Al Ahly goal arrived from API-Football
  as 30'. Thirty rejected candidates retained a last-readable clock of 28',
  while Barcelona's official timeline labelled the goal 29'. Production
  checked 28 against a center of 30 and rejected it as two minutes away.
- **Cause:** The Python baseline and Go design normalize the provider's ordinal
  minute to the broadcast's completed-minute clock with
  `elapsed + extra - 1`. The Go implementation then clamped that result to at
  least `elapsed`; with no stoppage extra, the clamp always undid `-1`.
- **Implemented:** Remove the clamp without widening the configured ±1
  tolerance. Tests assert both `ExpectedMinute` and outcome for 1', 30',
  45+2', 47', and 90+4', including the accepted lower edge and a rejection one
  minute beyond it.
- **Validation evidence:** Among the already verified non-stoppage production
  shares, 143 of 165 sampled clocks were exactly `API elapsed - 1`, 20 equalled
  the API minute, and two were one minute ahead. This sample is
  validator-selected but confirms that minus one is the dominant live shape.
- **Rollout:** Release `136e2d2` deployed successfully on 2026-08-19 at
  19:10 UTC. Both workers, the API, and Twitter verified the exact immutable
  release identity. No database or object-store repair was required; existing
  completed discovery workflows remain historical, and new validations use the
  corrected clock center.

### FF-057 — scorebug period evidence was discarded before clock validation

- **Live evidence:** The Barcelona–Al Ahly broadcast used a reset clock with an
  explicit period label. Abdelkarim's API-30′ first-half goal showed
  `28:56 1st`; Zizo's API-51′ second-half goal showed `05:25 2nd`. The VLM wire
  contract requested only `MM:SS`, so the latter reached the evaluator as bare
  minute 5, was classified as first half, and rejected genuine-looking clips.
- **Earlier boundary evidence:** The integer parser also discarded the
  distinction between a bare running boundary, explicit `2H`/`ET`, and compact
  stoppage. `2H 00:30` became integer 45 and was classified H1; compact `45+2`
  became integer 47 and was classified H2. A normal API-46′ event normalizes to
  clock minute 45 and exposes the same collision.
- **Cause:** `FrameObservation` had no period field even though the parser
  understood period text embedded in `clock`. The constrained prompt required
  the model to emit only clock digits, guaranteeing that adjacent `1st`/`2nd`
  evidence disappeared before `Evaluate`.
- **Implementation:** The model now returns a nullable visible-period enum
  (`1H`/`2H`/`ET1`/`ET2`) per frame. A structured `ClockReading` retains the
  normalized absolute minute, pinned period, stoppage precision, frame index,
  and ambiguity. Visible `05:25 + 2H` normalizes to minute 50; continuous
  `50:25 + 2H` remains 50. Explicit wrong halves still reject. A plausible
  relative interpretation without visible period evidence can only enter the
  lower unverified pool—the API expectation never manufactures verification.
  Clock rejects persist all raw observations and normalized readings in the
  existing JSONB outcome detail; no schema migration is required.
- **Exact reset boundary:** An explicit `45:xx 2H` is structurally ambiguous:
  it can mean continuous time at the start of H2 or a clock that reset to
  `00:00` and reached the end of H2. The parser now retains both visual
  interpretations, 45 and 90, and lets the expected period/minute select a
  match within the unchanged ±1 tolerance. `15:xx ET2` similarly retains 120
  and 105. No ordinary minute gains a second interpretation.
- **Compatibility:** The model call count and 25/50/75 frame strategy are
  unchanged. The activity output is additive and nullable, so old Temporal
  payloads decode with `period=nil`; workflow command structure is unchanged.
- **Verification:** Focused domain, activity, and workflow suites cover both
  live scorebugs, continuous clocks, explicit conflicts, absent-period
  ambiguity, compact stoppage provenance, old payload decoding, and diagnostic
  persistence. On 2026-08-19, the deployed `gemma-4-12b` model accepted the
  strict nullable-period schema and correctly labelled all nine frames across
  three production-candidate replays. Abdelkarim's `28:51`–`28:57` frames were
  labelled `1H` and verified against API 30′; two Zizo clips with
  `05:24`–`05:29` clocks were labelled `2H`, normalized to minute 50, and
  verified against API 51′. Natural production validation remains before
  closure.
- **Boundary probe:** A real Gordon API-90′ reject was resolved and processed
  through the current three-frame/model/evaluator path. Gemma read
  `45:00`, `2H`, and a `00:10` stoppage sub-clock. The follow-up retained
  `[45, 90]` and verified minute 90; the previous evaluator had retained only
  45 and rejected the same shape.
- **Historical repair:** `scripts/replay_clock_rejects` plans the exact
  `clock present but does not match expected` selection, then—only in explicit
  apply mode—registers a separate deterministic checklist, preserves every
  prior verdict in `outcome_detail.replay`, checkpoints all search attempts,
  and runs the normal EventWorkflow sequentially. The Barcelona fixture plan
  is four events and 104 candidates.
- **First replay result:** Release `72f4f81` deployed at 21:12 UTC. The first
  replay workflow processed all 39 Abdelkarim candidates: 31 duplicates, one
  promotion, one intermediate promotion subsequently superseded, and six
  remaining clock rejects. The surviving verified clip is rank 1 at extracted
  minute 28 with popularity 30; the two prior unverified shares remain active.
  The runner then failed its audit-count assertion and did not prepare
  Raphinha, Zizo, or Gordon.
- **Replay audit boundary:** A nil terminal detail reached Postgres as JSON
  `null`, not SQL `NULL`. Concatenating the replay object therefore produced
  `[null, {"replay": ...}]` for 31 duplicate outcomes. No evidence was lost,
  but the promised object path was malformed. The terminal UPSERT now treats
  either null representation as an empty object. Rerunning the same identity
  normalizes only the exact two-element arrays owned by that workflow before
  verification; focused and full integration gates pass.
- **Completed repair:** Release `70fca8f` deployed at 21:27 UTC with its exact
  identity verified in both workers, the API, and Twitter. The resumed run
  normalized exactly 31 Abdelkarim envelopes, verified the completed 39-row
  identity, and processed the remaining 15 Raphinha, 18 Zizo, and 32 Gordon
  candidates sequentially. All four checklists closed as `assets_surfaced`;
  the persisted replay counts are `39/15/18/32`, with zero pending candidates
  and zero malformed arrays. Active shares now number `3/4/3/6` respectively;
  verified rank-one clips exist for Abdelkarim, Zizo, and Gordon, while
  Raphinha retains two verified clips at ranks one and two. The workflows
  emitted every promotion through the normal publication activity, logged no
  warnings or errors, and created no Firefox fleet container. Natural
  production validation remains required before closure.
- **Rollout:** Release `e9c3c54` deployed successfully on 2026-08-19 at 20:21
  UTC. Both workers, the API, and Twitter verified the exact immutable release
  identity. Both workers registered, all three Temporal schedules remained
  active, and the first post-release active-poll cycle completed with zero
  errors. The release changed no database schema or object-store state and
  stranded no Firefox fleet instance.

### FF-055 — live leader flags survive a drawn result

- **Observed:** A 2026-08-19 production audit found 12 completed draws among 60
  played completed fixtures. Ten retained a non-null winner inconsistent with
  their tied score. No sampled non-draw completed fixture had a winner that
  contradicted its score.
- **Cause:** API-Football reports `teams.*.winner` for the current leader during
  live play and returns `null` / `null` while tied. `Fixture.UpdateWinners`
  ignored null inputs, so an equalizer could not clear the stored leader. The
  archived Python implementation carried the same incorrect final-only
  assumption.
- **Implemented:** Ordinary and `AET` winner state now derives from aggregate
  score; `PEN` derives from the shootout; ties and incomplete scores produce
  `null` / `null`. Exceptional terminal results use the provider's exact
  nullable flags because their scores are not authoritative. Ingest and active
  reconcile share this domain operation. A `PEN` completion vote and the final
  SQL readiness check both require present, non-tied penalty scores.
- **Tests:** Domain tables cover home/away/tied/missing, shootout, and
  exceptional outcomes. Monitor regression covers a stored 1–0 leader followed
  by a 1–1 response. Provider-vote and Postgres truth tables cover absent,
  tied, and decided shootouts.
- **Rollout:** Release `5962dd2` deployed successfully on 2026-08-19 at
  17:52 UTC. Both workers, the API, and Twitter verified the exact immutable
  release identity. A guarded transaction then cleared winner state on the ten
  verified stale `FT` draws and aborted unless all ten still matched the tied
  score and stale-winner predicates. A single production `fixture.update`
  invalidation reached the connected subscriber; direct API verification
  returned `winner: null` for both teams on all ten fixtures. A natural future
  lead-to-tie transition remains the production behavioral proof. See the
  [decision record](./decisions/2026-08-19-winner-state-is-derived-from-canonical-scores.md).

### FF-054 — remove dormant webhook and outbox schema surfaces

- **Proof:** Production code has no query, repository, handler, or worker for
  `webhook_subscriptions`, `webhook_deliveries`, or `outbox_cursor`; only
  `schema.sql` and its schema-shape test reference them.
- **Boundary:** Deleting them from `schema.sql` changes the embedded hash and
  blocks worker/API startup against every durable database until an explicit
  migration drops the live objects and stamps the new hash.
- **Sequence:** Finish the current FF-041 hash-version migration convergence,
  then use the existing one-migration-at-a-time flat-schema contract. Do not
  conceal this operational change inside a code-layout rollout.
- **Schema-comment debt:** `schema.sql` still describes provider winner flags
  as final-only and mentions the removed completion fast-path. The schema hash
  fingerprints comments, so correcting those bytes by itself would block every
  durable environment. Refresh them inside this same planned migration and
  stamp change rather than manufacture a comment-only production migration.

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
| `AUD-0813-CF-153` | Promoted to FF-059: current VNC contradicts the locked raw-Firefox recovery decision; implement and prove the corrected path before waiting for another expiry. |
| `AUD-TWITTER-COOKIE-WRITER` | Atomic rename prevents corruption, but concurrent fleet writers remain semantic last-writer-wins. Capture real rotation evidence before adding a cross-container lock or single-writer controller. |
| `AUD-0813-CF-175` | Decide whether national-team coverage needs an explicit seed beyond league-derived rosters. |
| `AUD-0813-CF-179` | Measure public playback before restoring unused `ffmpeg.Faststart`. |
| `AUD-0813-CF-SLO` | Define a match-coverage SLO before adding summary storage or alerts. |
| `AUD-0813-CF-SCORE` | Decide whether clients need score-at-detection history. |
| `AUD-0813-P3-14` | Measure rank-rebalance cost at real event sizes before replacing the simpler full rebalance. |
| `AUD-DESIGN-TRACING` | Add distributed tracing only when a concrete cross-service diagnostic requires it. |

Do not schedule global coverage floors or a generated log catalog. FF-061 now
classifies page/network evidence and preserves usable-search accounting, but the
incident still does not identify X's exact response class. Choose a global rate
limiter or fixed backoff policy only after a natural recurrence supplies that
evidence.

## Behavior that is intentional

- Goals, red cards, and missed penalties are intended to run the full discovery
  workflow, including 15 usable Twitter search observations. FF-061 enforces
  that contract; unavailable feeds and activity errors consume only the
  separate bounded outage budget.
- Perceptual dedup is event-scoped and category-scoped. Do not classify the
  lack of general cross-event fuzzy dedup as a bug without new evidence.
- The archived Python implementation is a behavioral reference, not the Go
  architecture template.
