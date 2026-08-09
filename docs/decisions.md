# Architectural Decisions

Append-only. Newest at top. Dates in `YYYY-MM-DD`. When migrating an old
decision because the rationale changed, leave the old entry in place and
add a new one above it pointing at the change.

---

## 2026-08-09 — Category-scoped dedup is post-vision (shipped gate diverged; #171 rescoped)

Re-reading Python + the signed-off [`video-dedup.md`](design/proposals/video-dedup.md)
against the shipped `event_pipeline.go` surfaced that the EventWorkflow consumer
silently diverged from the intended dedup methodology. This supersedes the narrow
framing in the **2026-08-05** entry below (#171 was "wire `quality.go`") — the real
fix reorders the pipeline. Flagged in [audit-2026-08-05](design/audit-2026-08-05.md)
(Tier-1 #1 + line 80). The methodology itself was already fully specified —
[`python-functional-spec.md`](design/python-functional-spec.md) §3 (verified/unverified
pools) + §7 (vision-before-perceptual ordering: *"the rebuild should preserve this
exact ordering"*), the **(pre-history) Scoped deduplication by `timestamp_verified`**
entry below (with the real prod case: a Goal-1 clip wrongly replaced by a Goal-2
clip, ~31′ vs 15′), and [`roadmap.md`](roadmap.md) — the shipped consumer just
didn't follow it.

**Methodology (already designed — `video-dedup.md:139`; Python `upload_workflow.py:321-331`):**

- **Category-scoped perceptual dedup.** Verified (clock-matched) clips dedup ONLY
  vs verified; unverified (no clock) ONLY vs unverified — never cross. *Why:* dHash
  matches visual similarity, and one broadcast yields similar frames across
  **different moments**, so an unverified different-moment clip can perceptually
  "match" the verified clip of THIS goal. The clock is the only ground-truth
  pinning a clip to this goal; the partition stops a different-moment clip
  collapsing onto it.
- **Verified always ranks above unverified** (any popularity); popularity/quality
  sort within a pool (`CompareShares`).
- **`IsUpgrade` quality winner-selection applies WITHIN a pool** — cross-pool there
  is no comparison, only ranking.

**Order (category is unknown until vision → perceptual dedup MUST be post-vision):**

- gate (`onVideoDone`): **md5-exact dedup only** (byte-identical ⇒ same category ⇒
  safe) → drop + bump popularity, else fire vision.
- post-vision (`onVisionDone`): reject → drop; verified/unverified → perceptual
  dedup within the SAME pool → `IsUpgrade` winner-select in the matched cluster →
  promote / supersede / consolidate.

**Shipped divergence (what #171 fixes):** `event_pipeline.go` ran perceptual
`video.Match` at the GATE — pre-vision, category-blind, keep-first. Risks
collapsing two different goals of one match (on a frame-hash false positive) and
never upgrades a clip. #171: md5-only gate; perceptual + winner-select move to the
serial post-vision path, pool-scoped.

**Cost premise corrected.** `video-dedup.md` sequenced vision-before-hash because
"perceptual hashing costs more than a vision call." In the as-built Go (streamed
ffmpeg extraction + parallel `VideoWorkflow` children) hashing is cheap and
parallel, so that argument does NOT hold — the reorder is a **correctness**
requirement, not a cost win. Hashing stays in the child for every clip; only the
dedup *decision* moves post-vision.

**Tradeoff:** vision now runs on every md5-unique clip (Python parity) vs only
perceptually-unique ones — more joi calls, bounded (not blocked) by joi's
2-concurrent cap. A pre-vision "perceptual pre-cluster + deferred collapse" could
reclaim them if launch load demands; parked. The A/B gate question (whether to
vision an unverified incumbent's dups) is **dropped** — moot, since pools never
cross.

**Phase-1 groundwork shipped + still valid** (now pool-scoped): schema `superseded`
share-state + widened CHECK; `Assets.MarkSuperseded` + `Assets.AddPopularity`;
`Shares.MarkSuperseded`; test fakes.

## 2026-08-07 — Doc-restructure divergence backfill (audit-2026-08-05)

Recording deliberate as-built divergences a future reader might otherwise
"restore" as regressions (doc-restructure Phase 4):

- **Video hard-filter retunes.** `MinDurationSecs` is 5 (not Python's 3); a
  `MinFrameRate` gate was added (broadcast is 24/25/30/50/60); the pre-download
  short-edge check moved into the post-probe `HardFilter`; download is cookieless
  (syndication adapter, not the browser). All intentional — see
  [`design/proposals/video-dedup.md`](design/proposals/video-dedup.md) +
  [`design/v-phase-orchestration.md`](design/v-phase-orchestration.md).
- **Ingest fail-open + national-team scope (G2/G3, parked).** `FetchFixturesForDay`
  fails OPEN on an empty tracked set (returns every fixture) and the default
  tracked set under-covers FIFA national teams vs the charter. Both are known
  gaps (#174/#175), deliberately parked — do NOT assume the filter is complete.
- **Popularity is a stored `+1` counter, not `COUNT(shares)`.** The `+1` on
  collapse is non-idempotent (a retry over-counts) and a pending-clip collapse
  skips the bump (#171-adjacent). A soft ranking signal — documented so it isn't
  "fixed" into a different semantic by mistake.
- **Alias resolution is resolve-once / permanent** (no 30-day TTL; `IsFresh` is
  dead code) — see [`design/proposals/team-aliases.md`](design/proposals/team-aliases.md).

## 2026-08-06 — Worker memory: mem_limits, streamed frame extraction, ffmpeg↔mem coupling

Coupled changes from a load test on real-worst-case clips (audit
[2026-08-05](design/audit-2026-08-05.md) G5 + the Firefox-leak dig).

**Container mem_limits (all found-footy dev services).** The dev compose declared
none — a host mem-budget-rule violation. Added circuit-breaker ceilings (worker
16g, twitter 8g, postgres 4g, temporal-ui 1g, rest 2g): well above real peak so
nothing chokes, low enough that a leak/runaway can't eat luv's 125g shared with
other stacks. Recorded in `vedanta-dhobley/docs/memory-budget.md`.

**Rejected: a Temporal `MaxConcurrentActivities` cap** (G5's literal suggestion).
Wrong tool — it never binds under real load and protects only Temporal's own
scheduling, not the host. The real gaps were the missing mem_limit above + the
unbounded per-clip memory below.

**Streamed dense frame extraction.** `ExtractDenseFrames` buffered the whole PNG
stream and returned `[]Frame` (~314 MB for a 90 s 1080p clip; the load test showed
8 concurrent ≈ 7 g, dominated by held frames). Refactored to a callback that hashes
each frame to an 8-byte dHash the instant it's parsed off ffmpeg's stdout, so peak
Go heap is ~one frame. PNG bytes are byte-identical (`TestStreamPNGs` asserts it) →
dHash values unchanged, no re-baseline needed. After this, ffmpeg RSS (~0.5 g
worst-case) is the only per-clip cost.

**ffmpeg concurrency ↔ worker memory coupling.** `FFMPEG_MAX_CONCURRENT` 4→32 (the
semaphore already existed as `FFmpegConfig.MaxProcesses`) + `THREADS_PER_PROC` 2→1,
so 32 procs × 1 thread = 32 = 1:1 with luv's 32 hardware threads (past that is
oversubscription, not throughput — ffmpeg extraction is CPU-bound). Worker
mem_limit 8g→16g = 32 × ~0.5 g worst-case; a rare all-worst-case flurry degrades
gracefully (an OOM-killed ffmpeg child just fails its activity → Temporal retry).

**Firefox memory leak (twitter).** A 7 h dev instance held 3.8 g RSS (95 % anon
heap) vs a ~400 MB fresh floor — unbounded in-memory caches (disk cache off, no
cap, Firefox sizing against the uncapped container's 125 g). Added cache-bounding
prefs (`browser.cache.memory.capacity` 50 MB, image surface cache 100 MB, media
cache 32 MB, bfcache off). The mem_limit is the hard backstop; [#160](design/proposals/twitter-scaling.md)'s
short-lived per-event instances are the structural fix. NOTE: hitting the twitter
mem_limit OOM-kills Firefox (not PID 1) with no relaunch watchdog → the service
**wedges**, so it's a never-hit backstop, not graceful recovery (relaunch-on-death
is #170-adjacent).

## 2026-08-06 — Twitter scaling: one Firefox per event (supersedes pool + router)

Full design in [`design/proposals/twitter-scaling.md`](design/proposals/twitter-scaling.md).
Replaces the shared-pool + client-side-router + `docker --scale` reconcile-loop
scaler (twitter-port.md T/d–T/e) with **one twitter/Firefox instance per active
event**, provisioned and released by its own EventWorkflow (per-event checkout).

Rationale: it removes the router entirely (each workflow holds its instance
address), removes search contention (dedicated browser per event — the exact
single-browser bottleneck that timed out every search on 08-05), and makes
instances short-lived (lifetime = event lifetime ~15 min) so the Firefox memory
leak never accumulates. **Zero warm**: the ~60–90s debounce window hides the
~30s instance startup, so spinning up at debounce-start yields a ready instance
by trigger — a persistent warm browser would only idle-leak + stale cookies.

Guiding principle locked in: **the monitor is the only state-poller.**
Everything downstream is event-driven off its detections (goal appears → spawn +
provision; goal disappears → cancel + revoke). The only clock-driven things are
timers, not state-polls: the daily ingest and the **cookie keep-alive** (a
fixture-independent daily refresh so idle stretches don't stale the session —
must NOT live on the monitor, which is idle when no fixtures play). VAR
cancellation (#172) folds in as event-driven off the removal detection. Max cap
~8; docker-socket provisioning; open decisions (cap, past-cap behavior, keep-
alive cadence) in the doc.

## 2026-08-06 — Stale dev DB was the *second* reason the pipeline never produced a clip

Found during the first live end-to-end run (Inter Miami friendly). The dev
postgres volume was **never re-provisioned after #166** (CLAUDE.md warns to wipe
the volume on schema edits — it wasn't), so the running `video_assets` still had
the pre-#166 schema (`perceptual_hash`, no `event_id`/`frame_hashes`) while the
code wrote the current schema → **every `PromoteAndPersist` failed** on a missing
`event_id` column. So the video pipeline had *never* saved a clip in this DB.

Combined with the 10s twitter-client cap (2026-08-05 entry), **two independent
bugs both blocked clips and masked each other** for weeks — the timeout hid the
schema mismatch because searches never got far enough to promote. Fix: wiped +
re-provisioned the app postgres volume (guarded to spare the Temporal volume),
re-ingested the fixture; the monitor re-detected the goals (cumulative in the
API) and they promoted cleanly. First clips ever landed — 7-goal match, real
Messi goals → gemma-validated ranked shares, zero errors.

Process lesson (motivates the as-built audit): the as-built docs described a
schema the running DB didn't have, and nothing caught it. The audit's schema.sql
↔ running-DB check exists to catch exactly this.

## 2026-08-05 — Quality-aware dedup winner selection (metadata score; built, not yet wired)

`internal/domain/video/quality.go` scores dedup-cluster members to pick the best
clip to keep, from metadata captured at download time, in priority order: capped-
relative **duration** (Python's 15% band, flattened past a 60 s cap so a padded
clip earns no edge but is never dropped for length) → **bits-per-pixel**
(bitrate / (w·h) — the deliberate improvement over Python's `file_size` proxy,
which a blurry upscale games) → **resolution**. `IsUpgrade(challenger, incumbent)`
is the guard; ties and marginal wins keep the incumbent (stability).

**Status: built + unit-tested, but NOT wired.** The consumer's `collapse()` is
still keep-first — `IsUpgrade`/`ClipQuality` and the `superseded_by` column are
called nowhere in the persist path, so a later higher-quality duplicate does not
replace the incumbent. Wiring it (supersede on win, popularity merge, share
removal) is #171. See [`design/audit-2026-08-05.md`](design/audit-2026-08-05.md)
Tier-1 #1. (This is the entry `quality.go`'s header cites.)

## 2026-08-05 — Twitter search: uncap the worker's HTTP client (a 10s cap strangled every search)

The Go video pipeline had produced ZERO clips in ~13 days of the dev stack
running. Root-caused in a live run (Napoli / Augsburg / Celta friendlies):
the worker's twitter `Client` shared one `http.Client{Timeout: 10s}` for the
startup `/health` probe AND every `Search`/`Download`, with a comment
claiming "per-method calls override." They don't — Go's `http.Client.Timeout`
is a hard cap on the *whole* request and is NOT lifted by a per-request
`context.WithTimeout`; the shorter of the two wins.

A real search takes ~11–30s+. An isolated empty search measured **11.2s**:
~2–3s nav + auth-verify, then ~8s waiting `tweetFeedTimeout` for a tweet that
never renders (empty result); result-bearing searches add a 2s hydrate sleep
+ deliberate per-scroll stealth jitter × up to 10 scrolls. So every search
exceeded the 10s cap and failed with "Client.Timeout exceeded while awaiting
headers" — never once completing.

**Fix:** the shared client is uncapped (`http.Client{}`); each method bounds
itself via context as originally intended — `Search`=SearchTimeout (120s),
`Download`=DownloadTimeout, and `probeHealth` gets its own 10s context so
startup stays bounded. A direct `/search` curl confirmed the search path is
otherwise healthy (HTTP 200, clean, 11.2s).

This was the single blocker to a working pipeline. Secondary findings from
the same run, tracked separately: Firefox memory leak (missing
`suspend-bkgnd-video` pref — fixed; mem_limit + cycling in #160), worker
one-shot twitter-client wiring (#170), single-browser throughput / no
scaling (#160). Follow-up search-latency tuning (tighten scroll jitter to
~0.25–0.5s, replace the 2s hydrate sleep with an event-driven wait) next.

## 2026-08-05 — Unknown-scorer goals: placeholder at debounce 0 + hard-delete (Python parity; corrects a Go divergence)

Surfaced by the first live end-to-end run: the dev stack (already up) was
monitoring a live Napoli–Osasuna preseason friendly and detected a Napoli goal
at minute 27 — but API-Football reported it with **no scorer** (`player=None`,
confirmed via a direct `/fixtures/events` call; happens on some friendlies).
The Go reconcile debounced that scorer-less goal to 3, flipped
`downstream_triggered`, and **spawned a doomed EventWorkflow** that bailed
`unknown_player` with 0 search attempts.

That diverged from the Python system (`archive/src/activities/monitor.py`),
which treats an unknown scorer as **"not a full event yet"**:
`initial_count = 1 if player_known else 0` (unknown seeds 0, no
`monitor_workflows` entry, no spawn), and — the `unknown_scorer_disappeared`
exception — an unknown-scorer event is **deleted immediately** when it
disappears, not routed through the 3-miss VAR decrement.

**Decision — port Python's model exactly (three-state debounce):**

- **Unknown scorer** (`Player.Known()` false) → placeholder at
  `debounce_count = 0`, no presence vote, no `event.detected` emit, no spawn.
  Pinned at 0 while present.
- **Known scorer** → seeds 1 + first vote, debounces 1→3, flips
  `downstream_triggered`, spawns.
- **Placeholder disappears** (superseded once the vendor attributes a scorer —
  a *new* player-keyed `natural_key` appears) → **hard-deleted**
  (`EventRepo.DeleteUnknownEvent`), NOT the soft-delete/VAR path.

**Why hard-delete, not soft-delete** (verified before implementing):

- `removal_reason` is `var | policy | asset_gone` — all "a *confirmed* event
  was revoked." A never-confirmed placeholder doesn't belong in that
  vocabulary; a new `refined` value would just pollute it.
- Routing placeholders through `RegisterEventAbsence` would mis-stamp
  `removed_reason='var'`, emit a misleading `event.removed`, and overload the
  count-0 state (placeholder vs VAR-decremented-to-0).
- `debounce_count` already permits 0 (`CHECK BETWEEN 0 AND 3`), so no schema
  change. Every `events` child FK is `ON DELETE CASCADE` except
  `video_shares.event_id` (`RESTRICT`) — and a placeholder never mints a share,
  so the RESTRICT can't fire and stands as a fail-loud guard. `DeleteUnknownEvent`
  is guarded on `debounce_count = 0` so it can never touch a confirmed event.

**Consequence for La Liga (launch target):** real competitions attribute
scorers within a poll or two, so the placeholder window is brief; the fix just
prevents the doomed spawn and the ~60s re-debounce noise. `unknown_player`
remains the correct terminal state only for goals a competition never attributes
(friendlies) — no team-only search fallback (noisy, low value).

Deliberately deferred: `player_id` is *in* the `natural_key`, which is what
forces the drop-and-re-debounce (the known event re-proves a goal the
placeholder already saw, ~60s lag). A player-less `natural_key` (stable goal
identity + in-place refinement) would remove that, but it's a schema/seq change
with its own edge cases — out of scope this close to launch.

Code: `internal/infra/pg/event_repo.go` (`Insert` count-gate + `DeleteUnknownEvent`),
`internal/domain/event/repo.go` (interface), `internal/activity/monitor/activities.go`
(`ReconcileFixture` matching-skip + absence-divert + spawn guard),
`internal/workflow/active_poll.go` (`unknown_dropped` metric). Regression:
`test/scenarios/edge_cases/player_refinement.yaml` rewritten to the new
behavior (1 surviving event, not 2).

## 2026-08-04 — API + eventing shape for cutover (Chi; timezone-agnostic; fixture-level push via JetStream)

Settled in a design pass while scoping #167 (read API) + the frontend eventing path.

**Read API stack: Chi, NOT Chi+Huma.** rebuild-plan §8 penciled in Huma (auto-OpenAPI +
struct validation), but the read surface is a handful of GETs consumed by one client we
control (vedanta-systems). Huma's machinery isn't worth its weight — use plain Chi (router +
middleware) and hand-shape JSON to match the frontend. Add Huma only if a documented public
API for third parties ever materializes.

**API is timezone-agnostic — no `date` param.** Date bucketing depends on the viewer's
timezone (a frontend concern — vedanta-systems already does `getDayBounds` + `offsetMin`).
The API returns fixtures with UTC kickoff timestamps over a bounded/state window; the frontend
groups into its local days. A `date` param would smuggle a timezone assumption into the backend.

**Update granularity: the hint names the smallest changed scope; the frontend refetches THAT.**
Every event carries `fixture_id` (always) + `event_id` (nullable). **Event-level** changes (goal,
new video, rank change) set `event_id` → frontend refetches `GET /api/v1/events/{event_id}`.
**Fixture-level** changes (activation, completion, ingestion — no specific event) have no
`event_id` → refetch `GET /api/v1/fixtures/{id}`. One uniform rule, two granularities —
standardized AND surgical. Replaces Python's coarse "refetch EVERYTHING every ~30s"
(`notify_frontend_refresh` fired every monitor cycle + every download workflow — massively
unoptimized). NOTE: refetch granularity is **payload-driven** (the `event_id`), NOT a JetStream
feature — JetStream's subject-level filtering is a separate consumer-side optimization the SSE
bridge doesn't need (it forwards everything).

**Subject scheme** — dhobley standard `<project>.<domain>.<event_type>` (topology.md). found-footy's
current 2-part subjects (`event.detected`, `fixture.completed`, …) gain the **`found_footy.`**
prefix → `found_footy.event.detected` etc. (`found_footy`, not `found-footy` — matches the existing
`rank_recalculated` underscore-token style + is identifier-safe for stream/account names). IDs stay
in the payload, not the subject (per-ID subject filtering deferred per "don't design speculatively").
Folded into #169.

**`POST /refresh` deprecated entirely.** found-footy no longer calls the frontend over HTTP; it
publishes to the event bus and the frontend refetches on hint. The frontend needs no inbound API.

**Eventing: JetStream for the durable event stream** (per [dhobley decisions 2026-08-04](../../../vedanta-dhobley/docs/decisions.md) — cross-project pattern):
- Composer swaps `nats.Publish` → `js.Publish` onto a `FOUND_FOOTY` stream over `event.>`/`fixture.>`
  (at-least-once). The vedanta-systems SSE bridge becomes a **durable JetStream consumer** that
  auto-replays on reconnect. JetStream persists to NATS's own file store — **no Postgres**.
- **`event_log` becomes audit-only** — its SSE-reconnect-backfill role moves to JetStream. Keep for
  SQL-queryable history, or drop later.
- **No `GET /events?since=` backfill endpoint** — JetStream handles reconnect-replay, so the read API
  doesn't serve backfill. One fewer endpoint in #167.
- The composer→JetStream swap is a **small fast-follow, NOT launch-blocking**: for launch the frontend
  can poll `GET /fixtures/{id}`; the push upgrades latency and is best built once (during cutover),
  not core-NATS-then-migrated.

**Read API endpoint list (#167):** `GET /api/v1/fixtures/{id}` (fixture-scope refetch),
`GET /api/v1/events/{event_id}` (event-scope refetch), `GET /api/v1/fixtures` (bounded/by-state
window; frontend buckets by tz), `GET /api/v1/videos/{share_id}` (→ 302 presigned S3 URL),
+ share lookup for og-server. See [design/api-contract.md](./design/api-contract.md).

---

## 2026-08-04 — #164c-b: EventWorkflow producer/consumer engine (the V-phase spine)

**Decision.** `EventWorkflow` grows from a search-only workflow into the full
per-goal orchestrator, per [`design/v-phase-orchestration.md`](./design/v-phase-orchestration.md).

- **Producer** (`workflow.Go` coroutine): the existing search loop, now spawning
  one `VideoWorkflow` child per new candidate and registering its future on a
  shared `workflow.Selector`. Sets `searchDone` after the last attempt.
- **Consumer** (`event_pipeline.go`, the main coroutine): drains the Selector —
  `onVideoDone` runs dedup (md5 exact → `video.Match` sliding-window over the
  frame sequence, against `assets ∪ pending`), collapses dups (BumpPopularity +
  DeleteStaging), and fires `ValidateClip` for a genuinely-new clip;
  `onVisionDone` promotes verified/unverified clips (`PromoteAndPersist`) or
  drops rejected ones (DeleteStaging). Single-threaded by the Selector → the
  dedup step is race-free with no locks.
- **Completion:** `searchDone && inFlight==0` — the `run()` loop uses
  `HasPending()` + `workflow.Await` so it never blocks on Select when nothing is
  ready but the producer is still working (and completes instantly for a
  zero-candidate event). No idle timeout — Python's 5-minute tail is gone.

**Implementation decisions:**
- **Dedup thresholds** (`MaxHamming/MinRunFrames/MaxGapFrames`) surfaced through
  the start-of-workflow `GetDiscoveryConfig` read → recorded in history →
  replay-deterministic. `video.Match` is pure; a future *algorithm* change needs
  a `GetVersion` guard (not added yet — fresh launch, no in-flight workflows).
- **`Extra` plumbed** onto `EventWorkflowInput` (from `event.Extra`) so vision
  validates stoppage-goal clocks (45+N / 90+N) correctly — without it the clock
  check mis-validates. Populated in the spawner.
- **inFlight decrements on EVERY callback path incl. failures** (child error,
  vision error) — a skipped decrement would hang the workflow forever.
- **`pending` set** holds deduped-new clips whose vision is in flight, so a
  later dup deduping during that window still collapses (closes the async race).

**Tested:** WorkflowTestSuite — search-loop behavior preserved (child mocked to
"rejected"), plus a verify+dedup pipeline test (two same-md5 clips → one
promoted, one collapsed, `PromoteAndPersist` called once, `assets_surfaced`).
The V-phase is now an end-to-end pipeline in code (not yet run against a live
goal — that's the deploy/cutover step #168).

---

## 2026-08-03 — #164c-a: DiscoveryWorkflow → EventWorkflow rename (Option 2)

**Decision.** The workflow that Monitor spawns per goal is renamed
`DiscoveryWorkflow` → `EventWorkflow`. Its job grew from *just search* to
orchestrating the **whole event** (search → video → dedup → vision → persist,
plus future phases like sentiment), so "Discovery" undersold it. Behavior-
neutral rename commit (full suite green proves it); the producer/consumer logic
lands in #164c-b.

**What renamed (→ event):** the workflow func + `EventWorkflowInput/Output`,
`internal/workflow/discovery.go` → `event.go`, the spawner method
`SpawnDiscovery` → `SpawnEvent`, the `StartWorkflow` type string, the
deterministic workflow ID prefix `discovery-{id}` → `event-{id}`, registration.

**What stayed `discovery` (the *phase*, not the workflow):** the
`internal/activity/discovery` package (`SearchTweets`/`StoreCandidate`/…), the
`internal/domain/discovery` query builder, `DiscoveryConfig` + `DISCOVERY_*`
env, and — deliberately — the pg **`event_downstream_workflows.workflow_type`
value `'discovery'`** (+ its `EventsAwaitingDiscovery` recovery query). That
value is filtered by a live query and is a fine internal label for "the event's
downstream workflow"; renaming it would spread into the event domain interface
for no gain. Documented at the write site.

**Why Option 2 (workflow=event, phase=discovery):** name a thing for what it
*is*. The workflow is about the event; the search machinery is about discovery.
A future *sentiment* phase reinforced this — EventWorkflow will orchestrate
multiple phases (video-discovery, sentiment), so the event-orchestrator name is
future-proof, and sentiment gets its own accurate name (not "discovery"). The
`EventWorkflowInput` type stays in `activity/discovery` (cycle-free shared home
— `workflow` imports `activity/monitor`, so the type can't live in `workflow`).

*As-built ledgers besides orchestration.md still say DiscoveryWorkflow; they get
reconciled in #164c-b when the full EventWorkflow behavior is documented.*

---

## 2026-08-03 — #164b: consumer-queue persist activities + a combine deviation

**Decision.** The EventWorkflow consumer's post-dedup/vision steps ship as
`PersistActivities` (`internal/activity/video/persist.go`): `PromoteAndPersist`,
`BumpAssetPopularity`, `DeleteStaging`. Plus a server-side S3 `Copy` (CopyObject)
on the adapter for the staging→assets promote.

- **Deviation from v-phase-orchestration.md:** the doc listed "Promote" and
  "Insert asset + share + rank" as *two* queue steps. They're **combined into
  one `PromoteAndPersist` activity** because the asset UUID can't be minted in
  workflow code (non-deterministic), and that UUID drives *both* the assets S3
  key and the DB row — so copy + insert must share one activity where
  non-determinism is allowed.
- **Retry idempotency by design:** the asset UUID is **derived** from
  `(event_id, md5)` via uuid v5, so a retried activity produces the same UUID →
  the same assets key (copy is a no-op overwrite) and the same row (`InsertAsset`
  is ON CONFLICT). The share mint is guarded by a "share already exists for this
  asset?" check. `BumpAssetPopularity` is the one non-idempotent step (a retry
  may over-count by 1) — benign for a soft vote signal, noted not fixed.

**Scope:** activities + S3 Copy + tests (fakes for persist, testcontainers for
Copy). Wired into `cmd/worker`. The EventWorkflow that *calls* these is 164c.

---

## 2026-08-03 — #166 schema revision: dedup moves out of the DB into workflow code

**Decision.** `video_assets` stops arbitrating perceptual dedup; the DB now
enforces only what it honestly can (exact-byte uniqueness), and the fuzzy
perceptual dedup lives in EventWorkflow code.

- **Dropped:** the single `perceptual_hash BYTEA` column, `perceptual_hash_prefix`,
  the `video_assets_hash_prefix` LSH index, and `UNIQUE (event_id, perceptual_hash)`.
  A SQL `UNIQUE` can only test *exact* equality of one hash; real near-dupes
  (re-encode / watermark / offset) never collide on a single hash, so the
  constraint fired ~never on the dupes we care about — it was dedup-shaped but
  didn't dedup.
- **Added:** `frame_hashes BYTEA NOT NULL` — the per-frame dHash *sequence*
  (8 bytes big-endian per 0.1 s frame; count = `octet_length/8`). Stored as a
  queryable **record** (debugging / re-dedup / analysis), NOT a decision-maker.
  The real dedup is the offset/gap-tolerant sliding-window `video.Match` over
  this sequence, run **in workflow memory before insert**.
- **Kept:** `UNIQUE (event_id, md5)` — honest exact-byte dedup, and it doubles
  as insert idempotency.
- **`AssetRepo` port redesign:** removed `GetByPerceptualHash`,
  `UpsertWithHashDedup`, `FindNearMatches` (all assumed the DB was the dedup
  engine); added `InsertAsset` (INSERT … ON CONFLICT (event_id, md5) DO NOTHING)
  + `BumpPopularity`. Domain `Asset`: `PerceptualHash`/`Prefix` → `FrameHashes []uint64`.

**Scope:** domain + schema only — the ports have no pg implementation yet
(that lands with #164's InsertAsset). schema.sql is the fresh-launch source of
truth (no migration file — launching fresh for La Liga). Validated: pg
integration test re-applies the schema + inserts `frame_hashes` clean.

**Why now:** unblocks #164 (EventWorkflow), whose promote step needs the new
insert path. See [`design/v-phase-orchestration.md`](./design/v-phase-orchestration.md)
"Resolved since".

---

## 2026-07-28 — V/4 clip validation: vision-LLM soccer/screen gate + clock check (rungs 1–3 shipped)

**Decision.** The V-phase clip-validation activity ships across three rungs,
with the model config, prompt, and strictness rules **validated on real prod
clips** (a gemma/Qwen bake-off, not asserted from the stale Python notes):

- **Rung 1 — LLM adapter plumb.** `llm.ChatRequest` gains `ResponseFormat`
  (structured output → `response_format:json_schema`, `json.RawMessage`
  straight through) + `DisableThinking` (→ `chat_template_kwargs:{enable_thinking:false}`).
  Domain-owned types; the mechanism stays out of `ChatRequest`. First (and
  today only) LLM consumer.
- **Rung 2 — `internal/domain/vision`.** Ported clock parsers
  (`parseClockField`/`parseAddedField`/`parseStoppageClockField`, the last
  stripping a leading `+`) + `periodOf` + `Evaluate` (soccer/screen majority
  gates → period-aware clock check → verified/unverified/rejected). Pure,
  table-tested incl. real-prod + WC-final boundary cases.
- **Rung 3 — `internal/activity/vision`.** `ValidateClip`: fetch staged clip →
  `ffmpeg.ExtractFrame` @25/50/75% → ONE multi-image structured-output call →
  `Evaluate`. Verdict is a nil-error Outcome; only infra/model failures are
  errors. Wired into `cmd/worker`.

**Model config (locked by the bake-off).** Multi-frame **single** call
(25/50/75% in one message) + **thinking off** (`chat_template_kwargs`) +
`response_format:json_schema` + temp 0. Thinking-off cut latency ~3× (34s→~13s
gemma) with no accuracy loss — the schema leaves no room for reasoning tokens.
The **detailed prompt is load-bearing**: the careful `screen` definition and
the frozen-clock/sub-timer description are what fix the false-accepts and
capture the stoppage `+1:48` sub-timer (terse prompts dropped both).

**Model choice.** `gemma-4-12b` on nexus is the production model (user's
call); `Qwen3.5-9B` on joi matched it 4/4 and was *faster* (~9s vs ~13s), so
we're **not model-locked** — the config is the lever, not the model. Vision is
the LLM's only consumer, so `LLM_ENDPOINT_URL` is repointed at nexus/gemma
(`.env.example`); no separate vision client / metrics split needed. A
joi-RAG + nexus-vision split is a documented follow-up if RAG ever lands.

**API period semantics — verified, not assumed.** Real API-Football data
(WC-2022 final) confirms `(elapsed, extra)` is **consistent at every
boundary**: stoppage caps `elapsed` at 45/90/105/120 with `extra=N`; running
ET uses `elapsed` 91–120, `extra=null`. So period is cleanly derivable for
**all** boundaries — my earlier "ET is inconsistent" claim was wrong (based on
one mis-read spec example). The real ET difficulty is *broadcast clock
rendering*, not the API.

**Strictness (settled).** (1) **±1 minute** tolerance (`VISION_TOLERANCE_MINUTES`);
not loosened — two goals can be a minute apart. (2) **Period guard: strict at
halftime** (H1/H2 clean on both sides → hard-reject wrong-half), **lenient at
extra time** (ET rendering varies → numeric-match-but-period-conflict is
soft-kept as `unverified`, not dropped). Frozen-boundary stoppage with no
sub-timer verifies on period alone (exact minute unpinnable; same-window goals
~never confused). OCR leading-digit rebase kept.

**Real-prod win.** On both linked prod goals (Lauberbach 90+2, Yeboah 71'),
prod surfaces a clean broadcast *and* a phone-of-TV recording, and the
shareable link defaults to the **screen recording**. gemma flags `screen=true`
on both → our gate rejects them and keeps the clean broadcasts. Strictly
better than what's live. 8/8 real clips correct end-to-end.

**Not yet wired into a workflow** — `ValidateClip` runs once per perceptual
cluster inside the (still-open) EventWorkflow orchestration; that's the next
V-phase step, not this one.

---

## 2026-07-28 — V-phase dedup: dHash + gap-tolerant window, params empirically validated (pHash rejected)

**Decision.** Perceptual video dedup is **dHash + an offset-tolerant,
gap-tolerant sliding window**. Starting parameters (all env-tunable —
`config.DedupConfig` / `HardFilterConfig`; *final* calibration is a live-data
job, these are validated-safe starts):

| knob | value | env |
|---|---|---|
| per-frame hamming | ≤ 10 | `DEDUP_MAX_HAMMING` |
| match window | 30-frame run (3 s @ 0.1 s) | `DEDUP_MIN_RUN_FRAMES` |
| gap tolerance | ≤ 3 missed frames in the window | `DEDUP_MAX_GAP_FRAMES` |
| sampling interval | 0.1 s | `DEDUP_FRAME_INTERVAL_SECS` |
| min clip length | 5 s | `HARDFILTER_MIN_DURATION_SECS` |

**How we got here — a bake-off on real Dybala footage + ffmpeg-transformed
variants, measuring the sliding-window match run:**

- **pHash rejected — the premise was contradicted by data.** Across two
  *different* watermarks over the same footage, **dHash matched perfectly
  (46/46); pHash failed (0–5).** dHash's 9×8 downscale washes out a localized
  overlay; pHash's low-frequency DCT is *sensitive* to a solid box. pHash also
  doesn't fix the real gap (crops). So **keep dHash** — closes the earlier
  "dHash→pHash rung-2 revisit," and it's the good outcome (no work, better
  result).
- **dHash is robust to layout-*preserving* transforms** (scale / compression /
  watermark — matched) and **fragile to layout-*shifting* ones** (crop / zoom —
  failed). **Crop-fragility is an accepted limitation:** the same goal at
  different crops won't collapse → some near-duplicate assets surface
  (mitigated, not eliminated, by ranking + the LLM quality-pick). Crops occupy a
  distinct hamming band (15–19), between matches (≤10) and non-matches (≥23), so
  a future crop-detector could target it without colliding with different
  footage.
- **Thresholds validated.** True matches (scale / compress / watermark /
  temporal offset + their compound) sit at per-frame median hamming **1–10**;
  different footage at **23–29** — a 13-bit gap, so 10 is well-centred.
- **Temporal offset handled.** A worst-case half-interval (0.12 s) shift only
  moved median hamming to 4; the realistic compound (shift + heavy recompress)
  to 8 — still inside 10 (run 31 @ 0.1 s). **0.1 s** (finer than Python's
  0.25 s; affordable now that hashing is single-pass + parallel and *vision*,
  not hashing, is the bottleneck) gives comfortable alignment margin.
- **Gap-tolerant window (new vs Python's strict run).** "≥ minRun frames with
  ≤ maxGaps misses" bridges a single bad frame so it doesn't shatter a real
  multi-second match. On both a 5.1 s and an 11.5 s clip under the realistic
  compound the tolerant window hit **50–55** frames; different footage tops out
  at exactly maxGaps (**3**) — huge separation, the 30 threshold safely between.

**Non-matches never produce a run, so the 30-frame requirement costs no
*measured* precision** on clearly-different footage — it's **insurance** against
an unmeasured near-miss case (two genuinely-different-but-similar clips) whose
real calibration needs live same-match data. We bias strict deliberately: the
failure directions favour it — too-strict → an occasional *duplicate shown*
(mild); too-loose → a genuinely different clip *wrongly merged and dropped*
(lost content).

**Clock/no-clock dedup pools emerge for free.** Because dHash is crop-fragile, a
scorebug-cropped (no-clock) clip does NOT perceptually match its uncropped
(clock) twin → they cluster separately automatically. So the clock-status
segmentation the pipeline wants happens *via clustering* — which means
**dedup-before-vision holds** (vision runs once per cluster → 1× joi, not 2×),
and it resolves the scorebug-crop-vs-clock-check tension (cropped clips aren't
dropped, they're a lower-ranked pool).

**Caveat — synthetic.** All transforms were ffmpeg-generated on real clips, not
two real reposts (the sampled real clips were all distinct footage). Mechanisms
are clear and consistent, but final tuning is a **live-match-day** exercise:
instrument dedup to log real run/hamming distributions for same-goal and
different-goal pairs, then set the knobs from evidence.

`Match` moved from strict-consecutive to the gap-tolerant window
(`domain/video/match.go`); the pHash + probe experiment code was deleted after
recording this.

---

## 2026-07-27 — V-phase rung 3b: per-candidate activities (staging-split, pre-download filter)

**Decision.** `internal/activity/video` — two activities:
- **`DownloadAndStage`**: ResolveVideo → **pre-download filter** (aspect +
  duration from the tweet-result, reject portrait/compilation with 0 bytes
  fetched) → download to worker-local scratch with **md5 inline**
  (`io.MultiWriter(file, md5)`) → ProbeMetadata → full `HardFilter` → stage raw
  bytes to Garage `staging/<fixture>/<event>/<tweet>.mp4`. Returns
  `{md5, stagingKey, metadata, outcome}`.
- **`HashVideo`**: fetch from Garage staging → dense extract → `DHashPNG` each
  → `[]uint64` sequence.

**Split at the staging boundary** so a `HashVideo` retry re-fetches from Garage
(internal, cheap) rather than re-hitting Twitter — the expensive external
download happens exactly once (user's retry-granularity catch, see the
discussion that produced this).

**Outcome-vs-error, no NonRetryable machinery.** Terminal-but-normal verdicts
(passed / hard-filter reject / geo-restricted / deleted / no-variant /
malformed / corrupt) are **Outcomes** (nil error); only genuine transients
(rate-limit / CDN-timeout / S3 blip / ffmpeg timeout) are errors the workflow's
`RetryPolicy` bounds. Matches the codebase's plain-errors + RetryPolicy
convention — the project uses no `NonRetryableApplicationError`.

**Wiring.** The ffmpeg client (rung 1 adapter) is constructed in `cmd/worker`
now that an activity consumes it; syndication + s3 clients reused. Deps are
interface-shaped so the 8 unit tests inject fakes (covers every outcome/error
branch + the pre-filter download-skip). Full download→stage→hash e2e against
dev Garage is deferred to rung 5, where the child workflow runs it in Temporal
naturally (the pieces are individually validated: T/f download, r1/r2 ffmpeg +
dHash on real clips, S4 s3).

**Forward-note — dHash → pHash (user-flagged 2026-07-27).** `HashVideo` is
**algorithm-agnostic** — it emits a `[]uint64` frame-hash sequence regardless
of which perceptual hash produces it. The user expects to move to **pHash**
(DCT-based, robust to the watermarks/logos that goal clips will always carry;
dHash's adjacent-pixel edges are perturbed by overlays). That's a planned
focused **rung-2 revisit** (swap `DHashPNG`→`PHashPNG`, re-tune hamming +
consecutive thresholds on real *watermarked* clips) — it does NOT touch rung 3b.
Separately, the **LLM quality-ranking** step (pick the best clip *within* a
matched cluster — distinct from matching) is already sketched as `video-dedup.md`
Stage 8; it needs fleshing out in the dedup design pass (rung 6).

---

## 2026-07-27 — V-phase rung 3a: hard-filter + the aspect band (1.75–1.82, hard gate)

**Decision.** `domain/video.HardFilter` + `config.VideoConfig` /
`HardFilterConfig` — the pre-hashing metadata gate (rung 3a). Short-circuit
order: duration (3–90 s) → aspect (1.75–1.82) → framerate (≥ 20) → short-edge
(≥ 600 px). A reject is a normal **outcome** (a greppable reason slug), not an
error. All thresholds env-tunable.

**Aspect max loosened 1.80 → 1.82, on real data.** Sampled the aspect
distribution of two real goals' clips (cookieless resolve, no download):
- **Dybala/Roma** (18 clips): landscape-dominant; the *biggest* cluster was
  **1.817** (1170×644 — broadcast rips with the scorebug cropped, incl.
  verified @CentralASRoma). The old 1.80 max kept 7/15 landscape; **1.82 keeps
  13/15.**
- **Chiesa/Liverpool** (15 clips): portrait-dominant (9/15 were 4:5 / 9:16 IG
  reformats); all 5 landscape clips were clean 16:9 (1.778) — 1.80 vs 1.82
  identical.

So 1.82 nearly doubles recall on landscape-cropped goals and costs nothing on
clean ones. Env override kept for retightening.

**Portrait stays a HARD reject — three reasons.** (1) In the real-time window
(tight `max_age`, minutes after the goal) people post straight broadcast
*landscape*; the 4:5 / 9:16 flood is the *later* social-remix layer a live
search never sees — our test only surfaced it because we bumped `max_age` to
days. (2) It cheaply cuts portrait phone-of-TV recordings (the *landscape*
phone-of-TV case is 16:9 and indistinguishable by aspect — that's the LLM's
job, imperfect). (3) **dHash stretches the whole frame into a fixed 9×8 grid**,
so different aspects yield different hashes → a 16:9 and a 4:5 crop of the same
goal would NOT dedup; a tight band keeps framing consistent so dedup stays
reliable (1.778 vs 1.817 is ~2% → hamming stays under 10 → still collapses).

**Tested.** 13 table cases driven by the real Dybala/Chiesa dimensions
(landscape passes incl. 1.817; portrait / square / ultrawide / short / long /
low-fps / tiny / degenerate all reject with the right reason).

---

## 2026-07-27 — V-phase rung 2: perceptual dHash + offset-tolerant matcher (algorithm parity, not bit parity)

**Decision.** `internal/domain/video/hash.go` + `match.go` — the perceptual
dedup primitives (rung 2). `DHash`/`DHashPNG`: grayscale (ITU-R 601) →
histogram equalize → 9×8 area-downscale → 64-bit difference hash. `Match`:
offset-tolerant sliding window — at some integer frame offset, ≥
`MinConsecutive` frames agree within `MaxHamming` bits. Thresholds live in
`config.DedupConfig` (env `DEDUP_FRAME_INTERVAL_SECS=0.25`,
`DEDUP_MAX_HAMMING=10`, `DEDUP_MIN_CONSECUTIVE=3`) — Python's tuned values,
exposed for retuning without a rebuild.

**Algorithm parity, not bit parity.** We reproduce Python's *algorithm*
(grayscale → equalize → downscale → diff), not PIL's exact LANCZOS/equalize
arithmetic. The rebuild starts on a fresh corpus (no Python-era hashes to
compare against), so what matters is internal consistency — one Go pipeline
hashes every clip — plus the thresholds fitting our hashes (validated on real
clusters). This corrects the rung-1 entry's "reproduce Python bit-for-bit"
wording: PNG-lossless dense frames are kept for *artifact-free input*, not for
parity.

**Integer-offset matcher (simplifies Python).** Python's `_dense_hashes_match`
ran an O(N⁴) timestamp-tolerance search because its per-frame `-ss` extraction
gave slightly non-uniform timestamps. Our single-pass fps extraction (rung 1)
yields *uniform* frames, so matching collapses to a clean integer-offset array
slide — behaviour-preserving, ~O((Na+Nb)·Na).

**Validated on real clips 2026-07-27** (ffmpeg 5.1.9, two Dybala clips):
- 1170×644, 11.6 s: dense 596 ms + dHash 46 frames 699 ms → **~1.3 s**
- 720×1280, 11.4 s: dense 1065 ms + dHash 45 frames 1000 ms → **~2.1 s**
- each clip self-matches; the two clips cross-match **false** (different
  footage — no false positive at the 10/3 thresholds).

dHash cost tracks pixel count (portrait ~22 ms/frame vs 15 ms) because
grayscale+equalize run full-res. Optimisation lever if ever needed: push
grayscale+downscale into the ffmpeg filter (emit small gray frames) — deferred;
current cost (~18 candidates × ~1.7 s ÷ semaphore 4 ≈ 8 s/goal) is fine.

**Tested.** 10 unit tests (dHash on solid/gradient/PNG-round-trip, Hamming,
match identical/offset/no-match/too-short/threshold) + the real-clip
extract+hash benchmark. Placement in `domain/video` (the `perceptual_hash` is
a property of a `video_asset`). The per-frame hash **store** + retiring
`UNIQUE(event_id, perceptual_hash)` stay for rung 6 (the schema revision).

---

## 2026-07-27 — V-phase rung 1: ffmpeg adapter — single-pass dense extraction, semaphore-capped

**Decision.** `internal/infra/ffmpeg` — the ffmpeg/ffprobe subprocess wrapper
(rung 1 of the V-phase build): `ProbeMetadata`, `ExtractFrame` (single JPEG
for the vision clock-check), `ExtractDenseFrames` (PNG frames for perceptual
hashing), `Faststart` (moov-atom remux), `Ping`. Follows the pg-adapter
template (Instruments + `RegisterMetrics` + typed errors + a `Retryable`
classifier). Config is `config.FFmpegConfig`, all env-tunable.

**Single-pass dense extraction (diverges from Python).** Python's `hashing.py`
spawned a *separate* ffmpeg per frame (~46 processes for an 11.5 s clip,
serial — the "takes forever" the user remembered). The Go adapter does ONE
decode pass (`-vf fps=1/interval`, image2pipe PNG) and splits the concatenated
PNG stream by walking chunk boundaries. Benchmarked 2026-07-27 on two real
Dybala clips (ffmpeg 5.1.9, `ThreadsPerProc=2`):
- 1170×644, 11.6 s: dense = 46 frames in **619 ms** (13.5 ms/frame)
- 720×1280, 11.4 s: dense = 45 frames in **1076 ms** (23.9 ms/frame)
plus ~50 ms probe + ~90 ms single-frame. That per-clip cost is what sizes the
semaphore + activity timeouts.

**Concurrency as a CPU governor.** A `MaxProcesses` semaphore
(`FFMPEG_MAX_CONCURRENT`, default 4) + a per-process thread cap
(`FFMPEG_THREADS_PER_PROC`, default 2) bound total ffmpeg thread load ≈
`MaxProcesses × ThreadsPerProc` on this shared 32-thread host. Both env-tunable
(no recompile); the knee-finding concurrency sweep on real clips tunes them
once the rung-3 `ProcessCandidate` fan-out exists.

**PNG for dense, JPEG for vision.** Dense frames are lossless PNG so rung-2
dHash can reproduce Python's hash bit-for-bit; the single vision frame is JPEG
(smaller — the model doesn't need lossless).

**Tested.** White-box unit tests (probe-JSON parse, PNG-stream split, error
taxonomy, seam-driven methods via a fake runner — no binary required) + a
`ffmpeg_integration`-tagged real-binary benchmark. The dHash/md5 *algorithm*
choice stays open for rung 2 — the adapter is agnostic (it only extracts +
probes). Per rebuild-plan §9; `Config` lives in `internal/config` (project
convention) rather than in-package as the plan sketch showed.

**Follow-up.** The worker image already carries ffmpeg 5.1.9 (+ ffprobe);
make that an explicit Dockerfile dependency when rung 3 wires the adapter into
an activity.

---

## 2026-07-27 — T/f syndication video download: cookieless-first; geo-restricted broadcaster clips are terminal but redundant

**Decision.** V-phase's per-candidate video download (T/f, task #161) ports
Python's syndication path (`archive/src/activities/download.py`) to Go as
`syndication.ResolveVideo` + `syndication.Download`: extract the tweet id,
reject sub-18-digit snowflakes early (2026-05-25 Paderborn-Wolfsburg
post-mortem), GET `cdn.syndication.twimg.com/tweet-result?id=<id>&token=x`,
pick the highest-bitrate mp4 variant, then byte-fetch it from
`video.twimg.com` with static browser headers (User-Agent + Referer +
Origin). **Cookieless.** Every failure class is a typed, `errors.Is`-able
error (`internal/infra/syndication/errors.go`).

**Why cookieless-first — cookies don't help (proven by experiment).**
Live probe 2026-07-27 against the Dybala/Roma friendly (fixture 1567750;
one search returned 18 candidates): 2 of 3 sampled clips downloaded as valid
mp4 cookieless (one `ext_tw_video`, one `amplify_video`); the third — DAZN
Italy's broadcaster clip (`amplify_video`) — returned 403 → `ErrGeoRestricted`.
So `amplify_video` is *not* the discriminator; broadcaster geo/auth-walling
is. Python DID pass cookies on every CDN fetch (full cookie string +
`x-csrf-token: ct0`), yet its own 403 handler is labelled "geo-blocked or
auth required" and logs `has_auth` — cookies did not reliably beat the 403
there either. **Resolved 2026-07-27 by live experiment:** re-fetching the DAZN
clip authed (auth_token + ct0 + `x-csrf-token`, all present) still 403s, and so
does its HLS manifest (`.m3u8` — the path the browser player streams),
cookieless *and* authed. So this 403 is a **hard IP-geo-block on every path**
(mp4 and HLS), enforced at the network edge before auth matters — not an
auth-wall. Cookies can't beat it; neither can mimicking the player. The only
lever is a foreign egress IP (regional proxy), out of scope. Conclusion:
cookieless is not a compromise — cookies add nothing here — and the
"workaround" for geo is candidate **redundancy**, not proxy infra (below).

**Why it doesn't block V-phase.** A single goal yields ~18 candidates; the
fan reposts download fine cookieless, so a geo-locked broadcaster *original*
is redundant loss, not a coverage gap. `ErrGeoRestricted` is
terminal-for-this-candidate by design — exactly what Python did. Chasing true
geo-blocks (foreign egress IP, HLS-manifest mimic, yt-dlp) is explicitly
last-resort, deferred until we observe an event where *all* candidates 403.

**Tested.** 10 mock unit tests (`video_test.go`, in-gate) cover id extraction,
snowflake reject, token + headers on the wire, best-variant selection, and the
full status→typed-error taxonomy. A build-tagged live probe
(`video_live_test.go`, `-tags live`, `LIVE_TWEET_URL=…`) validates the real
CDN shape + cookieless headers against a supplied tweet URL — excluded from
the normal suite.

Diverges from `docs/design/proposals/twitter-port.md`'s browser-extract T/f
sketch (superseded — the syndication path needs no browser).

---

## 2026-07-27 — V-phase orchestration: a per-event workflow owns completion (Temporal), Postgres is a mirror

**Decision.** V-phase is one **per-event orchestrator workflow** (the evolved
`DiscoveryWorkflow`) that owns a goal's whole pipeline: search → download +
validate (a **Video child workflow per candidate**) → per-event content dedup →
**serialized** upload. **Temporal owns completion** — the parent `await`s its
children; **Postgres is a queryable mirror**, not the completion authority. The
only completion-load-bearing pg row is the `poll → event-workflow` hop (the poll
is a short-lived scheduled workflow and can't parent a ~15-min child).

**Supersedes** the three-workflow (Discovery/Video/Asset) + signal-with-start +
queue-drain + cross-event-corpus design in
[`design/proposals/video-dedup.md`](./design/proposals/video-dedup.md). That
doc's dedup *algorithm* (dHash, offset-tolerant match, thresholds) is kept; its
*orchestration* is replaced.

**Why.**
- Workflow code is single-threaded + deterministic → doing dedup + upload in the
  parent is **race-free serialization for free** — deletes the AssetWorkflow +
  signal-queue + queue-drain machinery that only existed to fake a join across
  detached workflows.
- A candidate is a multi-step unit (download → metadata → filter → hash →
  clock-check) → **workflow-shaped**, so Video is a child workflow, not an
  activity (independently retryable + visible in the Temporal UI).
- A real parent-awaits-children gives **native completion**, which removes the
  hand-built completion contract's edge cases (recovery sweep, started-then-died)
  from the downstream chain.
- Dedup is per-event + content-hashed (`UNIQUE(event_id, md5/perceptual_hash)`),
  against *this event's* already-uploaded S3 assets (incremental upload). The
  content/share identity split fixes Python's URL-as-identity flaw (N same-content
  clips → 1 asset).
- Python (3.5/3.7-era learning code) reached for this but hand-rolled it badly:
  `start_child_workflow` + `ParentClosePolicy.ABANDON` + fire-and-forget (no real
  parenting), per-attempt download batches, a 5-minute **idle-timeout** upload
  completion, and URL-keyed dedup. This design is the un-shoddy version.

**Full design + open build-path questions:**
[`design/v-phase-orchestration.md`](./design/v-phase-orchestration.md).

---

## 2026-07-25 — Video dedup is per-EVENT only; cross-event / per-fixture dedup is dead

**Decision.** `video_assets` is scoped per **event**, not per fixture.
Dedup (exact-byte, content, perceptual) runs only *within* a single
tracked event's candidate set — goal, missed penalty, or red card. There
is no cross-event dedup, no S3-corpus dedup across events, no multi-share
of one asset across events. Schema: `UNIQUE (event_id, md5)` +
`UNIQUE (event_id, perceptual_hash)`; `popularity` is a within-event vote
count. `video_shares` is unchanged — it was already per-event.

**Why.** We tried per-fixture (cross-event) perceptual dedup in Python
while testing, and it **removed legitimate goal videos**: two distinct
goals in one match often have visually-similar broadcast clips (same
stadium, camera positions, celebration framing), so a fixture-scoped
perceptual hash collapsed them and dropped one real goal as a "duplicate"
of another. Per-event scoping makes that class of false-positive
structurally impossible.

The problem cross-event dedup was *trying* to solve — the same clip
surfacing under two events (Miami smoke test: one Polish-language
own-goal video landed as a candidate under both the R. Rios own goal and
the L. Suarez penalty, via overlapping team aliases) — is solved far
better by **timestamp extraction**: the vision clock-check rejects a clip
whose broadcast minute doesn't match the event's reported minute, so the
clip stays with the event it belongs to and is rejected from the other.
That is the real fix, and it works better for our goals than dedup ever
did. Cross-event dedup is **dead and not coming back.**

**Consequences.**
- Removal of an event is naive-delete of its own assets/shares (+ their
  S3 blobs) — no ref-counting, no orphan-GC, no cross-event race handling.
- `video_assets` gains `event_id` (`ON DELETE CASCADE`); keeps `fixture_id`
  as a denormalized convenience for the s3 path + prune queries.
- Touches the already-built `internal/domain/video` package (`Asset` gains
  `EventID`; `AssetRepo` dedup methods key on `event_id`) — the pg adapter
  for these repos is unbuilt, so no ripple beyond types + tests.
- Supersedes [`rebuild/design-improvements-2026-07-23.md`](design/design-improvements-2026-07-23.md)
  item #6 (now marked REJECTED) and the cross-event sections of
  [`rebuild/proposals/video-dedup.md`](design/proposals/video-dedup.md)
  (SUPERSEDED banner added; full body rewrite pending the docs reorg).

---

## 2026-07-24 — Event primary key is synthesized (team_player_type_seq); the VAR slot-shift is a known, accepted tradeoff

### Why we synthesize a key at all

**API-Football reports no stable per-event identifier.** A fixture's
events come back as a bare array with no `id` field (verified on the
`APIFixtureEvent` struct). To match "the same real-world goal" across
30 s monitor cycles — for debounce, presence/absence voting, and the
`event_downstream_workflows` FK — we must **assemble our own primary
key** from the event's attributes.

### The key, and why seq (not minute)

`natural_key = "{team_id}_{player_id}_{type}_{seq}"`, where `seq` is the
per-`(team, player, type)` ordinal within the current API event list
(1st goal by this player = 1, 2nd = 2, …). Fixture scope comes from the
FK, not the string.

Of the attributes available, none is perfectly stable:

- **minute — drifts.** The vendor adjusts the reported minute as a goal
  settles (44'→45', 55'→55+1'). If minute were in the key, each
  adjustment would look like a *new* event → old key absence-voted away,
  new key inserted fresh → **debounce restarts on every goal.** This is
  frequent, so minute is disqualified as a key. (Python's own
  `fixtures.py:112` comment records this exact reasoning.)
- **seq — shifts on removal.** seq is positional and recomputed each
  cycle. If an *earlier* same-`(team, player, type)` event is removed
  from the API while later ones remain, every later event's seq shifts
  down one.

We chose **seq**, accepting the shift, because minute-drift is the
common case and shift is the rare one.

### The accepted cost (the "VAR slot-shift")

If a player scores 2+ same-type goals AND an *earlier* one is
VAR-overturned *after* a later one already exists in pg, the later
goals re-map onto the wrong rows: the overturned goal's row keeps
receiving presence votes (never removed → its video is kept), and the
last real goal's row stops being produced (absence-voted to 0 →
soft-removed → its video is lost). **Silent** — no crash, no error.

**Why it's low-risk (not the scary-P1 the audit filed it as):**
VAR resolves a goal within ~1–3 min, well before the same player scores
again, so the bad interleaving (goal A triggered → goal B triggered →
*then* A overturned) requires an unusually delayed VAR decision layered
on a brace/hat-trick — rare-squared. Impact is degraded, not
catastrophic: the Discovery query still carries the player name +
canonical team name regardless. And **Python has run this identical
design in prod without it surfacing as an incident** — we inherited the
tradeoff, we did not introduce it.

Note: Python's comment ("VAR'd events are deleted entirely, freeing the
sequence slot for new goals") describes the shift as if benign. It is
not — that "freed slot" is exactly what causes the re-map. This entry
corrects that record.

### The real fix (deferred — not Aug-14-critical)

Stop recomputing the key each cycle. **Freeze the key at first insert**
(it never changes thereafter) and match incoming API events to existing
rows by **fuzzy attribute proximity** — same `(team, player, type)`,
minute within a small tolerance — instead of an exact recomputed key.
That absorbs minute-drift (54' matches the existing 55' row) *and*
immunizes against seq-shift (matched by what the goal *is*, not where it
sits). It gives up strict O(1), but n ≤ ~10 goals/fixture, so an O(n)
scan per event is free.

Not pursued now: the minute-tolerance parameter is a genuine heuristic
with its own false-match edges (two distinct goals within tolerance),
it needs careful scenario testing, and the bug it fixes is rare +
degraded. Revisit post-Aug-14 if real match-day telemetry shows it
matters. Cheap interim option if desired: log a warning when a
`(team, player, type)` group's event *count decreases* mid-match (the
VAR-removal signature) — turns silent mis-map into a visible anomaly
without touching the key.

### Related

- [`internal/activity/monitor/activities.go`](../internal/activity/monitor/activities.go) — `ReconcileFixture` (set-diff), `seqCounterKey`, `buildDomainEvent`
- [`internal/domain/event/event.go`](../internal/domain/event/event.go) — `ComposeNaturalKey`
- `archive/src/data/fixtures.py:108-140` — Python original (same seq method, misleading VAR comment)
- [audit-2026-07-26.md](design/audit-2026-07-26.md) — filed this as P1; downgraded here to documented known-limitation

---

## 2026-07-24 — Club entity selection: reverted name-match, kept Wikipedia-rank (correct for the tracked roster)

Short-lived experiment reverted the same day. Do NOT re-attempt without
reading this.

**What happened.** `resolveClub` originally returns the **first
P31-passing hit in Wikipedia CirrusSearch rank order**. The audit found
"Sporting CP" resolving to Sporting CP B (the reserve team) — Wikipedia
ranks the B-team page above the multisport-umbrella senior page. I
replaced the selection with token-set name-matching against the api name
(commit e996265).

**Why it was reverted.** A full-roster resolver test (all 144 tracked
teams) caught that name-matching **regressed two tracked teams**:

- `Bayern München` → *Türkgücü München* (should be FC Bayern Munich). The
  api name is German (`München` → unidecode `munchen`); English Wikipedia
  says `Munich`, so a wrong club sharing the German spelling out-scored
  the correct one.
- `Athletic Club` → *Alumni Athletic Club* (Argentine; should be Athletic
  Bilbao, whose Wikipedia title is "Athletic Bilbao", not "Athletic Club").

Pure name-matching breaks whenever the api name doesn't token-match the
English Wikipedia title (foreign-language names, short forms). **The
original Wikipedia-rank behaviour is correct for every team we actually
track** — Wikipedia's relevance ranking gets the famous team right. The
Sporting bug only affects Portugal (Primeira Liga), which we do NOT
track. We traded a theoretical bug for two real ones, so: reverted.

**Sitelink-count ranking** was considered as the robust alternative
(FC Bayern 124 sitelinks vs Türkgücü 17; the main entity always has more)
but rejected: it hardcodes "always want the most-popular entity," which
is wrong for the legitimate case where api-football names a B team (e.g.
in a friendly). Neither single signal is complete.

**Reserve-suffix demotion (FIXED, follow-up commit).** A team whose
reserve/"II"/B page outranks the senior in Wikipedia search AND whose
reserve entity isn't tagged `Q2412834` in Wikidata was mis-resolving to
the reserve side — the reverted-code roster run showed 2 tracked teams
hit it (Hamburger SV → Hamburger SV II, VfB Stuttgart → VfB Stuttgart II).
Fixed with a narrow rule (`reserveMarker` + demotion in `resolveClub`):
a candidate whose title ends in a reserve suffix (`II`/`III`/`B`/`C`/…)
the api-football name lacks is demoted, kept only as a fallback if no
senior candidate passes. If api itself names the reserve (a friendly),
the marker matches on both sides and it is NOT demoted. This is the
consistent-convention subset; named reserves without the suffix
(Real Madrid Castilla, Barça Atlètic) are handled separately by
Wikipedia ranking matching the exact api name. No new API call, no
cross-language pitfall (unlike the reverted full name-match).

**Kept from the reverted work:** nothing in code. The unidecode tokenizer
change (446bf1c, separate commit) stands. The full-roster resolver test
script (`scripts/resolver_roster_test`) is kept — it caught this.

---

## 2026-07-24 — Tokenizer transliterates via unidecode (romanize, don't drop); Twitter search is stroke-insensitive

### Context

The alias/player tokenizer ([`internal/domain/alias/text.go`](../internal/domain/alias/text.go))
normalizes names to ASCII query tokens via NFD decomposition + combining-
mark strip. The audit (2026-07-26, finding P0) surfaced that
`TokenizePlayerName("S. Ødegaard")` returned `[]` — **empty**. Same for
Højlund, and for any name with `ø æ œ þ ð ł đ ħ` (Nordic, Icelandic,
Polish, Croatian, Maltese letters).

Root cause is a Unicode subtlety. NFD decomposes **accents** because
they are modelled as base-letter + combining-mark: `ö` = `o` + U+0308,
`é` = `e` + U+0301 — NFD splits them, the Mn-strip removes the mark,
ASCII survives. But **stroke / bar / ligature letters are atomic
codepoints** with no canonical decomposition: `ø` (U+00F8) is not
`o` + a combining stroke, it is one indivisible character. NFD leaves it
untouched, it survives as a rune > 127, and the tokenizer's non-ASCII
guard (which correctly drops genuinely non-Latin scripts — Arabic, CJK,
Cyrillic) drops it as collateral. The letter is Latin but *looks*
non-Latin to a rune-value check.

The original code documented this as a known limitation but scoped it to
team rosters ("no team's canonical form uses these") — missing that the
**same shared function tokenizes player names**, where Ødegaard (Arsenal
captain), Højlund (Man Utd striker), and Guðmundsson (Fiorentina — the
player from our own 2026-07-22 smoke test) all live. Every goal by these
players fired a team-aliases-only query with no name signal → thousands
of generic tweets → wasted discovery budget → LLM-validator flood.

### Decision (evolved same day — final form is a library)

**Step 1 (interim, commit f198a13):** a hand-rolled `foldExtendedLatin`
transliteration table (`ø→o æ→ae þ→th …`) applied before NFD. Fixed the
P0 immediately and validated the approach.

**Step 2 (final, this commit):** replaced the hand table AND the NFD
sequence AND the drop-non-Latin rule with a single transliteration pass
via **`gosimple/unidecode`** (v1.0.1, Apache-2.0). `tokenize` and
`lowerASCII` now call `unidecode.Unidecode(s)`. Rationale: the hand
table was reinventing — worse and incompletely — what a maintained
library does comprehensively. A library survey (2026-07-24) picked
gosimple/unidecode over mozillazg/go-unidecode (faithful Python port,
kept as fallback) and the dormant rainycape/unidecode (which gosimple
forked).

**The bigger decision this unlocked — romanize non-Latin, don't drop.**
With a real transliterator, non-Latin scripts no longer survive as
>127 runes to be dropped; they get **romanized**. So we deleted the
"drop non-Latin script" rule entirely. Empirically (2026-07-24):
Cyrillic `Спартак → spartak`, Korean `레알 → real` — real tokens English
tweets use, recall we previously threw away. CJK/Arabic romanize to
phonetic noise (`红魔 → hong mo`, `ريال → ryl mdryd`) that no English
tweet contains — but the tokenizer does NOT judge that. The
alias-selection pipeline's **≥2-language threshold voting is the
arbiter**: a token seen in one language's label is dropped as noise, one
corroborated across languages survives. "Romanize + let the threshold
decide" keeps the pipeline **fully dynamic** — no hardcoded roster, no
per-script special-casing (user decision 2026-07-24, chosen over a
curated static-alias file precisely because it stays dynamic).

`Normalize` (alias.go) stays a distinct pure-NFD diacritic-strip that
**preserves script** (Спартак stays Cyrillic) — it is a different
operation from the tokenizer's romanize, has its own contract + tests,
and no non-test callers.

### Why fold to ASCII (not search the special-char form)

Empirically verified against live Twitter 2026-07-24 (dev twitter
service, `/search`):

- Search ASCII `mbappe` → 14 results, **9 contained "Mbappé"** (é).
- Search ASCII `odegaard` → 16 results, **3 explicitly contained
  "Ødegaard"** (ø), plus 10 with plain "odegaard".

**Twitter's search engine is diacritic-AND-stroke-insensitive.** The
ASCII token is therefore not merely adequate but *optimal*: it catches
both the special-char writers (via Twitter's internal fold) and the
majority who type the plain form on English keyboards. Searching the
`ø`-form would be strictly worse — it would miss every ASCII typer if
Twitter's fold ever changed. This vindicates the whole fold-to-ASCII
normalization strategy; the only defect was an incomplete table.

### Scope note — this RESOLVES the "why drop at all" question

The interim entry flagged a second, "deliberate" drop (non-Latin scripts)
as a still-open question. Step 2 **closes it**: we don't drop by script
anymore, we romanize and let threshold voting decide. The remaining
tokenizer brittleness is NOT normalization — it's `isCamelConcat`
over-dropping Mc/Mac names (McTominay), the Wikidata entity-ranking bug
(Sporting CP → reserve team), and generic-token survival (fire/foot/ball).
Those are separate, tracked in
[`design-improvements-2026-07-23.md`](design/design-improvements-2026-07-23.md).

### Consequences

- `gosimple/unidecode` v1.0.1 added to go.mod. `tokenize` + `lowerASCII`
  reduced to a single `Unidecode()` call; the NFD/Mn-strip loop, the
  `hasNonASCII` drop, and the interim `foldExtendedLatin`/`extendedLatinFolder`
  all **deleted**. Net simpler.
- The "drop non-Latin script" rule is gone — Cyrillic/Greek/Korean now
  romanize and survive to threshold voting.
- Tests: `TokenizePlayerName` cases (Ødegaard/Højlund/Guðmundsson/Łukasz)
  kept — unidecode satisfies them; `TestTokenize_NonLatinScriptDropped`
  flipped to `_Romanized` (Спартак→spartak now KEPT); `TestFoldExtendedLatin`
  removed with the helper.
- `Normalize` reverted to pure NFD (preserve-script); `lowerASCII` now
  uses unidecode. Both still have zero non-test callers (dead-code
  cleanup candidate).
- Resolves audit-2026-07-26 P0.

### Related

- [`internal/domain/alias/text.go`](../internal/domain/alias/text.go) — `tokenize`, `lowerASCII` (unidecode)
- [audit-2026-07-26.md](design/audit-2026-07-26.md) — P0 finding
- [design-improvements-2026-07-23.md #25](design/design-improvements-2026-07-23.md) — the "why drop" discussion (now resolved) + isCamelConcat/entity-ranking follow-ups
- [twitter-search-query.md](design/proposals/twitter-search-query.md) — D8 tokenizer rules
- gosimple/unidecode: <https://github.com/gosimple/unidecode>

---

## 2026-07-23 — Discovery filter: wall-clock-relative sliding window, not server-side time bounds

### Context

DiscoveryWorkflow ([`internal/workflow/discovery.go`](../internal/workflow/discovery.go))
fires a fixed sequence of Twitter searches (currently 10 attempts × 60 s
spacing, moving to 15 × 60 s per #162) per detected event. Each attempt
returns a set of tweet candidates; T/c's `/search` endpoint filters those
candidates by tweet age using a client-side `max_age_minutes` cutoff (3 min
per [twitter-search-query.md D4](../docs/design/proposals/twitter-search-query.md#d4)),
measured against **current wall clock**.

An alternative was seriously proposed and tested during this session:
use Twitter's advanced-search operators `since:YYYY-MM-DD_HH:MM:SS_UTC`
and `until:...` to hard-bound the search server-side to an explicit time
range tied to the event's debounce-completion timestamp. Theoretically
this is cleaner:

- Deterministic — same query at any wall-clock time returns the same result set
- No dependency on Twitter's timeline-ordering staying "recent-first"
- Could theoretically extend Discovery to backscan events hours old
  (bounded by known debounce timestamp)

### Decision

**Wall-clock-relative filter kept. Server-side `since:`/`until:` bounds
rejected as empirically unreliable.**

### Evidence

Ran two live smoke tests during the 2026-07-22 session to compare the
two designs:

**Smoke #1 — bounded windows against a stale event.** Fiorentina
Gudmundsson goal, ~2 hours old at test time. Query pattern:

```
(gudmundsson OR fiorentina OR acf OR florentia OR viola)
    since:2026-07-22_19:22:00_UTC until:2026-07-22_19:25:00_UTC
    filter:videos
```

Ran 10 overlapping 3-min windows shifted by 1 min each (attempts 1–10
covering 19:22 → 19:34 UTC). Result: **3 unique tweets across 10
windows**. Same query submitted twice returned the same 3 tweets
deterministically. Tweets in those exact time ranges provably existed
(surface via unbounded queries) but Twitter's server-side filter silently
dropped them. Twitter's `since:`/`until:` operators are undocumented,
best-effort, and non-idempotent for identical queries.

**Smoke #2 — wall-clock filter against a live event.** Inter Miami
L. Suarez penalty goal, ~20 min old at test time. Same 15-min coverage
period done differently — 10 attempts × 60 s spacing with
`max_age_minutes=3` client-side filter. Query pattern:

```
(suarez OR inter OR miami OR intermiami OR imcf OR imfc OR herons) filter:videos
```

Result: **27 candidates surfaced across the 10 attempts**, ages 0.2–3.0 min.
**Real goal videos landed at attempts 5, 6, 8, 9, 10** — an @LARGOESPN
video at attempt 8 and a VQV_Futbol clip at attempt 10 would have been
missed entirely by a single-shot bounded window. Multi-attempt time-based
coverage caught candidates that server-side bounds could not.

Full smoke-test log: task #159 (2026-07-22, completed).

### Implications

- Discovery works only for real-time events (goal detected → immediate
  workflow spawn). Hours-old backscans are out of scope by design.
- Multi-attempt time-based coverage is load-bearing — a single search
  at debounce-completion misses candidates that only surface minutes
  later. The 15-attempt × 60 s design (#162) is the minimum viable
  coverage per this evidence.
- If Twitter's non-bounded timeline search ever degrades (recent-tweets
  no longer surface reliably in ordered order), we have no fallback —
  bounded search is already known to fail. This is a real risk to note
  but not to design against yet; monitor via candidate-count metrics
  once shipped.

### Follow-ups

- Bounded search MIGHT be viable for a future "scheduled rescan of
  yesterday's misses" background job — separate from live Discovery.
  Not shipping now, noted as an option if wall-clock recall ever falls
  below target and we need a recovery mechanism.
- Peak vulnerability window: goal happens, Discovery starts within
  ~90 s (debounce), then runs for 15 min. Any Twitter-side outage
  during that window silently drops candidates for that goal — no
  recovery. Live-only coverage means we accept transient outages
  as data loss.

### Related

- [twitter-search-query.md](../docs/design/proposals/twitter-search-query.md) — D4 (`max_age_minutes=3`), D1 (recall-first OR shape)
- [discovery.md](../docs/design/proposals/discovery.md) — signed-off Discovery design (2026-07-16)
- [`internal/workflow/discovery.go`](../internal/workflow/discovery.go) — `discoveryMaxAttempts`, `discoveryAttemptSpacing`, `discoveryMaxAgeMinutes` constants (moving to config per #162)
- [`internal/twitter/search.go`](../internal/twitter/search.go) — `max_age_minutes` client-side filter application

---

## 2026-07-23 — Twitter Firefox profile: ephemeral per-container (Python-shape), not shared volume

### Context

T/b.4's docker-compose (2026-07-22) set up a `twitter-profiles` Docker
volume shared across all headless twitter replicas, mounted at `/data`.
The intent was to preserve Firefox's cached browser data (WASM caches,
media compressors, safebrowsing DB) across container restarts for
warm-start on scale-out.

T/b.5 hardening #1 was originally planned as "per-instance profile
dir via hostname hash" — a subdir-per-replica scheme under the shared
volume to avoid Firefox's cross-process SQLite locking conflict.

During implementation, mid-session investigation surfaced that:

1. Firefox's `.parentlock` file at the profile root is a hard mutex —
   the SECOND Firefox process to start against the same profile dir
   refuses to launch, regardless of what we intend to write. Not an
   SQLite issue we could work around at the app layer.
2. Python's twitter service (~months in prod) uses **ephemeral
   per-container profiles** — no shared volume, each container's
   `/data/firefox-profile/` lives in the container's own writable
   layer (Docker copy-on-write isolation). No locking conflicts
   because no sharing.
3. The subdir-per-hostname approach solves the locking problem but
   introduces its own class of bugs:
   - Stale `.parentlock` files from unclean shutdowns block replica
     restart until manual cleanup.
   - Old `compatibility.ini` from previous Firefox versions triggers
     profile migration prompts on Playwright base-image upgrades.
   - Shared volume disk usage accumulates across scale-down cycles.
   - Cross-instance state coupling — one replica's corrupted subdir
     could affect neighbors via the shared volume.

### Decision

Adopt Python-shape ephemeral profiles for the headless twitter fleet:

- Remove `twitter-profiles` volume from both compose files.
- Each headless container's Firefox profile lives in its own writable
  layer at `/data/firefox-profile/` — isolated by Docker's copy-on-
  write, destroyed when container is removed.
- VNC container keeps its dedicated `twitter-vnc-profile` volume —
  needs cross-session persistence for operator's logged-in state.
- Cookie backup file (`~/.config/found-footy/twitter_cookies.json`)
  remains bind-mounted from host — this is our cross-instance auth-
  sharing layer, independent of Firefox's profile.

### Rationale

- **Simpler.** No subdir logic in code, no stale-lock cleanup, no
  shared-volume management. Compose is smaller.
- **Matches proven prod behavior.** Python has run this shape for
  months without profile-related incidents.
- **Robust to unclean crashes.** Container OOM / SIGKILL / panic
  destroys the writable layer; fresh restart gets clean state.
- **Firefox-version upgrades work cleanly.** Fresh profile matches
  fresh Firefox — no migration prompts, no compat.ini surprises.
- **Cold-start cost is manageable.** ~5-15s per container Firefox
  boot, mitigated further by disabling safebrowsing DB downloads
  (~200MB Google pull on first boot) via prefs — see hardening #2.
- **No shared state means no shared-state bugs.** Each container is
  an island; debugging is straightforward.

### Cost accepted

- ~10-15s slower cold start on scale-out (bounded, mitigated by
  disabling safebrowsing DB pull).
- No warm cache benefit on rolling restarts — but rolling restarts
  are rare, and the warm-cache benefit was overstated (Firefox's
  cache is regenerated on every page load anyway).

### Alternatives rejected

- **Subdirs per hostname hash on shared volume** — the original T/b.5
  #1 design. Introduces stale-lock and Firefox-version-upgrade
  fragility for a ~10s cold-start savings that's rarely realized in
  practice.
- **Named volumes per replica** (compose `deploy.replicas` + per-
  replica volume declaration) — doesn't scale beyond declared count;
  wouldn't survive scale-up past the initial declaration.
- **Firefox launched with `--headless --profile=/tmp/firefox-N`
  ephemeral tmpdir per instance** — same shape as our choice but
  under `/tmp` instead of `/data`. Not an improvement.

### Impact

- `docker-compose.dev.yml` — `twitter-profiles` volume removed, mount
  removed from twitter service, explanation comment added.
- `docker-compose.prod.yml` — same.
- `cmd/twitter/main.go` — `resolveProfileDir` hostname-hash function
  removed. TWITTER_PROFILE_DIR env stays with `/data/firefox-profile`
  default.
- `internal/twitter/browser.go` — `NewBrowserOptions.FirefoxUserPrefs`
  added to plumb the idle-CPU + safebrowsing-disable prefs from
  main.go into Playwright's LaunchPersistentContext.
- Deferred/reverted: no per-instance profile subdir logic, no stale-
  lock cleanup code, no shared-volume backup/cleanup runbook needed.

---

## 2026-07-22 — Query builder: D3 confirmed (no event vocabulary), sentiment_mode → video_only, own-goal invariant flagged

Written after the design conversation + empirical validation session
that culminated in the query builder shipping in
[`internal/domain/discovery/query_builder.go`](../internal/domain/discovery/query_builder.go).

### D3 (no event-type vocabulary) — REAFFIRMED with empirical evidence

The signed-off `twitter-search-query.md` D3 said the query is
identical for all trackable event types — no `goal` / `red card`
/ `penalty` vocabulary in the query itself. User pushed back on this
in the design conversation (2026-07-22), arguing OR-everything
should include event-type expansions because our OR-semantics makes
event tokens "free recall boost."

Ran the experiment. Findings:

1. **Some event tokens are safe** — `goal`, `scored`, `nets` didn't
   fire on non-football content in either quiet or fresh windows.
2. **Others are catastrophic noise generators** — `booked`/`booking`
   matched movie tickets ("40K+ tickets booked"), hotel bookings,
   Berlin tattoo appointments in a quiet Twitter window. `card`,
   `red`, `off`, `sent` all had strong theoretical noise potential.
3. **Real match test (Tottenham M. Fernandes goal today, bounded to
   the 3-min post-event window)** returned 4/4 legitimate goal-
   reaction tweets with player+team OR alone. Adding event terms
   under AND semantics REMOVED 3 of them (the "SUUUIII" celebration
   tweet, the "goa" typo tweet, and a general comment tweet). Adding
   event terms under OR semantics is unnecessary — every legit tweet
   already matched via player/team.
4. **User's revised requirement:** player token OR team alias MUST
   match. Event-only tweets should NOT include. Which structurally
   means event terms don't help — they can't match on their own.

Conclusion: D3 stands. Query is `(player_tokens OR team_aliases)
filter:videos`. Event vocabulary excluded. If real production data
shows systematic recall gaps for reaction tweets that don't name
player/team, revisit with data.

### D7 — SentimentMode renamed to VideoOnly (default true)

User feedback 2026-07-22: `SentimentMode` was awkwardly named
(described what it TURNED OFF rather than what it did). Renamed to
`VideoOnly` in `QueryInput`. Default `true` — matches Python's
current behavior (searches always include `filter:videos`). When
sentiment mode ships (fetching same corpus for text + video),
callers pass `VideoOnly: false` explicitly. No signed-off spec text
changed — this is an implementation-name refinement.

### Own goals — api-football does the swap; invariant flagged for verification

Per Python's archive (`archive/src/utils/event_enhancement.py`
`calculate_score_context` + `build_twitter_search`), Python treats
`event.team` at face value with NO swap logic for own goals — and
Python's Twitter search has been in prod for months without
complaints. This strongly implies api-football reports own goals
with `team_id = beneficiary team` (the one whose score increased)
and `player = the defender who scored into their own net`.

Our own `docs/api-football/events-shape.md` records this as an
"unverified but rumored behavior" pending live-sample validation.

For the query builder: **no special-case logic.** `event.player.name`
goes through `alias.TokenizePlayerName` (defender's name). `event.team`
resolves to `team_aliases[]` via the alias pipeline (beneficiary
team's aliases). Same code path as regular goals — the resulting
query catches BOTH the celebrating side (via team aliases) and the
scorer's name.

**Assumption to validate:** when the first own-goal event fires in
production, verify api-football's `team` field IS the beneficiary
(not the conceding team). If the assumption is wrong, fix belongs in
`internal/domain/event/` — the query builder itself doesn't need
to know.

### Testing methodology finding — bounded historical search is unreliable

Twitter's `since:`/`until:` operators work for FRESH events (proven
via the Tottenham test — 4/4 legit tweets in a 3-min bounded window
around a goal that happened minutes earlier) but silently fail for
older bounded queries. Proof: searching `the since:2026-07-19_20:00
until:2026-07-19_21:00` (extremely common word, 1-hour window 3
days back) returned 0 tweets. Same query without bounds surfaces
massive tweet volume.

Implication for production: **doesn't matter.** Real pipeline searches
seconds after events fire, always in the fresh window. Implication
for backfill / historical testing: **can't use bounded search for
retrospective analysis.** Deferred until we care.

### `max_age_minutes` is load-bearing

OR-everything's failure mode (Test 2 quiet-window returned 0 Enzo-
specific tweets and 3 unrelated Argentina reactions) is bounded in
production by the 3-minute client-side age cutoff. Without the age
filter, OR-everything is strictly worse than Python's AND-semantics
in quiet windows. T/c MUST ship with `max_age_minutes` support in
the search endpoint — it's not optional polish. Called out here so
future refactors don't drop it thinking it's cosmetic.

### Not decided this session (deferred)

- **Event-type token expansion revisit** — logged as "revisit if
  real match data shows we're missing legitimate reaction tweets
  that don't name player/team."
- **Storing search candidates for post-hoc learning** — task #156.
  Currently only `video_assets` + `tweet_intent` persist. Adding
  candidate storage (accepted + rejected) requires a schema
  addition; lands with Discovery workflow (O3).
- **`event_search_candidates` schema shape** — TBD in #156.

### Files touched

- `internal/domain/alias/text.go` — new `TokenizePlayerName(name)
  []string` helper. Combines the existing (unexported) `tokenize`
  with `isSkipped` filter + dedup. Exported for use by the
  Discovery-side query builder.
- `internal/domain/discovery/query_builder.go` — new. `BuildTwitterQuery`,
  `Build` ergonomic wrapper, `LengthWarn`, typed errors.
- `internal/domain/discovery/query_builder_test.go` — 18 tests
  covering D1/D4b/D4c/D4d/D7 + full D8 player-name table + own-goal
  invariant.
- `docs/architecture.md` — discovery/ flipped from ⊘ stub to
  ✓ query builder shipped.

---

## 2026-07-22 — Playwright-login validation: Twitter blocks Playwright login, raw-Firefox-subprocess fallback confirmed required

### Context

T/b landed with Playwright-Go non-headless as the login path in the
twitter-vnc container — the "MVP first" call from the [T/b.3 phase
proposal review](design/proposals/twitter-port.md). The bet was
that Playwright-instrumented Firefox might be acceptable to Twitter
at login time, letting us skip Python's raw-Firefox-subprocess trick
([`archive/twitter/session.py`](../archive/twitter/session.py):313
`_launch_manual_firefox`). If it worked, T/b.5's fallback code
wouldn't be needed. If not, we'd know quickly and add it.

Live experiment ran during T/b smoke testing:

- Moved cookies aside → twitter-vnc container came up unauthenticated
  (verified via `/status`; `EnsureAuthenticated`'s new
  unreadable-file path from this session's fix worked correctly).
- Opened `found-footy-dev-twitter-vnc.luv` in browser, saw Twitter
  login form (Playwright's auto-open at `x.com/` redirected to /login
  as expected).
- Entered `FoundFootyApp` in the username field, clicked Continue.
- Twitter responded: **"We've temporarily limited your login.
  Please try again later."** — before any password was entered.

### Decision

**Raw-Firefox-subprocess fallback is REQUIRED for the login path.**
Match Python's design pattern from
[`archive/twitter/session.py:313`](../archive/twitter/session.py):

- Login: `pkill firefox` → `firefox --new-instance -profile <dir>
  https://x.com/i/flow/login` via subprocess. Human logs in through
  the visible display; profile inherits cookies.
- Scraping + verify (session-time): Playwright, unchanged.

Implementation stays deferred (T/b.5 pending item) until either
(a) current cookies actually expire or (b) T/c ships. Rationale for
deferring: current cookies are healthy, T/c is higher-value work,
half-day of subprocess-lifecycle + Playwright teardown/relaunch code
is scoped and clear when we come back to it. Ship the structured
`auth_expired` log event alongside T/c work so we get alerting the
moment cookies fail.

### Rationale

- **Playwright-Go for scraping continues to work fine.** All-session
  observation: both dev containers healthy against Twitter with
  existing cookies, verify succeeds against x.com/home. The failure
  is at login specifically.
- **Python's archive documents the same failure mode.**
  [`docs/audit.md`](../archive/docs/audit.md) notes the abandoned
  `twitter/auth.py` module attempted automated login via Selenium
  and got accounts banned. Whether Playwright specifically has
  distinguishable fingerprints from Selenium at login time is
  academic — both trigger Twitter's login-time bot defenses, and
  Python's raw-Firefox-subprocess design is the mitigation that
  Python's actual prod uses today.
- **Session-time detection is much lighter than login-time
  detection.** Same Twitter, same account, same automation
  framework — cookies from a prior login work fine for months of
  scraping. What triggers defense is the login attempt itself. This
  is why raw-Firefox is only needed for the ~seconds-long login
  window, not for continuous browser operation.

### Open question — dev/prod account isolation

The experiment consumed a login-limit token against the shared
account. Since dev and prod share cookies (per
[twitter-port.md Q4 sign-off](design/proposals/twitter-port.md),
2026-07-16), dev experimentation can affect prod's ability to
re-authenticate when its cookies eventually expire. Not urgent
(cookies typically valid weeks-to-months; Twitter's login-limit
cooldown decays), but worth revisiting before La Liga launch
(2026-08-15):

- **Same account, throttle dev experimentation** (current): cheap,
  requires discipline about no gratuitous re-auth attempts in dev.
- **Separate dev account:** isolates dev experimentation. Accepts
  the multi-account correlation risk Q4 flagged (both accounts on
  same IP, similar activity patterns). Whether Twitter still
  correlates in 2026 is unclear.

Deferred as a pre-launch decision. Not blocking T/c.

### Impact

- `docs/design/proposals/twitter-port.md` revision log gets an entry
  noting today's experiment result.
- T/b.5 raw-Firefox-subprocess item stays pending but moves from
  "maybe needed" to "definitely needed, timing deferred."
- T/b.5 structured `auth_expired` log event moves up in priority —
  ship alongside T/c so we have alerting if cookies fail during
  T/c development or La Liga runup.
- No change to shipped code today.

### What was NOT changed

The Q4 shared-cookies decision (dev + prod share the same Twitter
account) STANDS. Today's experiment surfaces an argument for
revisiting, not a decision to reverse — see "Open question" above.

---

## 2026-07-22 — Twitter Dockerfile: one file, WITH_VNC-gated, matches Python's shape

### Context

Two twitter Dockerfiles existed on this branch and contradicted each
other:

- `docker/twitter/Dockerfile` — Playwright base image, installs the
  playwright-go Node driver. This is what T/a's PoC actually validated
  (standalone `docker build` + `docker run` passed the /health probe
  with state=unauthenticated). Only referenced by the dev compose file.
- Top-level `/Dockerfile` — Debian-based, parameterized on BINARY,
  gated a WITH_VNC=true build arg that installs x11vnc + novnc +
  websockify. Meant to be one Dockerfile serving all four binaries.
  **Broken for the twitter binary** because it didn't install the
  playwright-go driver — `playwright.Run()` would fail on first
  launch. Referenced by the prod compose file for twitter + twitter-vnc.

Nobody noticed the prod-compose brokenness because T/a was tested via
standalone build against `docker/twitter/Dockerfile`, and Phase F
kept `restart: "no"` on all Go services so nothing was actually
attempting to start.

The next work item (T/b.3, the VNC container image) forced the
reconciliation.

### Decision

**One Dockerfile per binary "shape"**, not per-service:

- `docker/twitter/Dockerfile` — Playwright base + driver install +
  optional WITH_VNC layer. Serves BOTH `twitter` (headless) and
  `twitter-vnc` (visible display). Behavior differs at runtime via
  `TWITTER_VNC_MODE=true` env var (compose sets per service);
  entrypoint script conditionally starts Xvfb + fluxbox + x11vnc +
  websockify+noVNC before exec'ing the binary.
- Top-level `/Dockerfile` — Debian base, worker/api/scaler only.
  Explicitly rejects `BINARY=twitter` with a clear error to prevent
  a future accidental regression.

This is the Python shape (one image, one binary, two modes distinguished
by entrypoint script) but leaner: the WITH_VNC=true build arg gates
the ~150 MB of VNC binaries so the headless twitter image doesn't
carry them. Python's monolith installs everything in every image.

### Runtime split

- Headless container (`twitter` service, compose default): no
  TWITTER_VNC_MODE, Playwright runs headless=true, no display
  server needed.
- VNC container (`twitter-vnc` service, opt-in via `profiles: [vnc]`):
  compose sets `TWITTER_VNC_MODE=true` + `TWITTER_HEADLESS=false`,
  entrypoint boots the VNC daemon stack, Playwright renders to the
  Xvfb display, operator sees it via noVNC in a browser.

### Rationale

- **Same code path scraping in both containers** — Python's most
  important structural property. Divergent binaries risk scraping-
  behavior drift.
- **Runtime env-var switch beats build-time flag** for the mode
  selection because compose is the natural authority on "which
  service runs which way" — one build produces an image that can
  serve either role (headless in prod at scale, visible for ops).
- **Cross-image parity** — headless and VNC images differ ONLY by
  the WITH_VNC layer + entrypoint's runtime branch. Any scraping
  bugfix ships to both by rebuilding the same source.

### Alternatives rejected

- **Two totally separate Dockerfiles** (Dockerfile.twitter +
  Dockerfile.twitter-vnc) — cleaner file-level separation but
  duplicates 90% of the content. Any bugfix to the shared parts
  requires two edits.
- **Two runtime targets in one Dockerfile via --target** — Docker
  multi-stage lets you pick a runtime stage, but the compose service
  definitions would need `target:` args and reading them cross-
  references. Runtime env-var branching is simpler and more visible.
- **Python's monolith** (install everything everywhere) — simpler
  but wastes ~150 MB on every headless image. We already committed
  to per-binary optimization for worker/api/scaler; extending to
  twitter for consistency.

### Impact

- Deleted: nothing (both Dockerfiles updated; the top-level one just
  loses its twitter-specific branches and gains a rejection guard).
- Changed: `docker/twitter/Dockerfile` — now the single canonical
  twitter image, gains WITH_VNC layer + entrypoint script hookup.
- New: `docker/twitter/entrypoint.sh` — bash script that boots the
  VNC daemon stack when `TWITTER_VNC_MODE=true`, otherwise passthrough.
- `docker-compose.dev.yml` — twitter service switched to reconciled
  Dockerfile; new twitter-vnc service (opt-in via profiles: [vnc]);
  `TWITTER_VNC_URL` + `TWITTER_VNC_START_CMD` env vars set on
  headless so /authenticate response surfaces them.
- `docker-compose.prod.yml` — twitter + twitter-vnc both switched to
  the reconciled Dockerfile (twitter was broken before this).
- `Makefile` — `twitter-vnc-up`, `twitter-vnc-down`, `twitter-vnc-logs`
  targets for one-command operator flow.
- Cross-repo `~/workspace/proxy/caddy/caddy.d/found-footy.caddy` —
  rename `found-footy-dev-twitter.luv` → `found-footy-dev-twitter-vnc.luv`
  to match the new dev container name. Flagged for user approval
  (cross-project edit).

---

## 2026-07-21 — VNC container is opt-in (not always running)

### Context

Twitter service's manual-login path uses a dedicated VNC container
(Xvfb + x11vnc + websockify + non-headless Firefox) for the operator
to log in when cookies expire. Original T/b plan had it running
continuously in dev + prod compose so operators could always click
through to it. User pushback during implementation: "I don't think it
should be always running? We can launch it ideally IF there's a
failure and I need to come over and log in."

### Decision

VNC container is opt-in. Docker compose entry uses
`profiles: ["vnc"]` so `docker compose up` skips it by default.
Operator runs `docker compose --profile vnc up twitter-vnc` (or a
Makefile target) only when auth actually needs re-establishing.

Impact on the service HTTP surface: `/authenticate` on headless
instances can't return a live VNC URL (VNC may not be running).
Instead it returns instructive guidance — includes
`TWITTER_VNC_URL` + `TWITTER_VNC_START_CMD` env vars in the response
so an operator sees:

```json
{
  "state": "unauthenticated",
  "action_required": "manual_reauth",
  "reauth_url": "http://twitter-vnc.luv:5900",
  "reauth_command": "make twitter-vnc-up",
  "message": "Twitter session expired. Start the VNC container: `make twitter-vnc-up`. Then log in at http://twitter-vnc.luv:5900."
}
```

Copy-paste-ready. When operator finishes login, VNC container writes
cookies to shared `/config/twitter_cookies.json`, headless fleet picks
up via mtime on next check (per 2026-07-21 filesystem mtime decision
below), and VNC container can be stopped.

### Rationale

- Zero always-on cost for the 99% of time when auth is fine.
- Same operator flow either way — auth breaks, operator gets alerted,
  operator does one docker command + one login. That command being
  "start VNC and log in" vs "just log in" is negligible extra effort.
- Keeps VNC container from being a persistent attack surface (Firefox
  + noVNC exposed even during quiet weeks).
- Coordinated with the filesystem-mtime coordination decision below:
  neither instance-to-instance nor instance-to-VNC needs a pub/sub
  layer; the shared file's mtime is the only signal anyone reads.

### Alternatives rejected

- **Always-on VNC container.** Simpler compose but strictly worse:
  running Firefox + noVNC for nothing 99% of the time, and adds
  attack surface. Only real advantage is "operator clicks through
  faster" — which the reauth_command in the response mitigates
  entirely.
- **Auto-start VNC when auth breaks.** Scaler or watchdog watches
  for `auth_expired` and spins up VNC. Reasonable extension for
  Phase M but not needed for T/b — operator will be in the loop
  either way (they have to physically log in).

### Impact

- `docker-compose.dev.yml` + `docker-compose.prod.yml` (T/b.4) —
  VNC service gets `profiles: ["vnc"]`.
- `Makefile` (T/b.4) — add `twitter-vnc-up` / `twitter-vnc-down`
  targets that shell into `docker compose --profile vnc up/down`.
- `internal/twitter/auth.go` (T/b.2, shipped) — `handleAuthenticate`
  reads `TWITTER_VNC_URL` + `TWITTER_VNC_START_CMD` env vars and
  surfaces them in the 503 response body. Tests cover both
  configured and unconfigured paths.

---

## 2026-07-21 — Twitter fleet coordination: filesystem mtime, not pg NOTIFY

**Supersedes (partial):** the pg NOTIFY portion of the 2026-07-21 entry
below. The broader "NATS scope: inter-project only" decision still
stands. What changed: after further design discussion, Twitter fleet
auth coordination doesn't need ANY pub/sub mechanism at all — not
NATS, not pg NOTIFY. Filesystem mtime IS the coordination signal.

### Context

The pg NOTIFY design assumed there was a real coordination problem
between headless Twitter instances: one instance detects auth failure,
needs to notify others. But every real cookie-failure scenario (Twitter
invalidates session, cookies expire by timestamp, password change, admin
revoke) causes ALL instances to fail simultaneously because they share
the same cookies for the same account. There's no case where "instance
A is logged out and instances B-C-D are still logged in."

So the "notify other instances of auth failure" broadcast was solving
a non-problem — each instance discovers the same failure on its own
next check within 60 seconds.

The ONE case where cross-instance signaling MIGHT help is telling
others "cookies were just refreshed" (either by VNC re-auth or by
another instance's successful search that got fresh Twitter refresh
tokens). But this is handled cleanly by filesystem mtime — each
`EnsureAuthenticated` stat's the backup file; if mtime is newer than
last-loaded, reload before doing the auth check. Python's own
`auto_verify_loop` (session.py:739) uses this exact mtime pattern for
a related purpose (detecting VNC login completion); Go generalizes it
for fleet coordination.

### Decision

**Twitter fleet coordination uses filesystem mtime.** Concretely:

- On each `EnsureAuthenticated`: `os.Stat` the cookie backup file. If
  `ModTime > lastLoadedMtime`, reload cookies from file, update
  `lastLoadedMtime`. Then proceed to warm-path or full verify.
- On each successful search: compute cookie fingerprint (sha256 of
  sorted name+value tuples). If unchanged from last-in-memory
  fingerprint, skip write. Else atomic write (temp+rename) with
  `auth_token` presence guard, update fingerprint.
- No pub/sub. No pg NOTIFY. No NATS. No message-bus wiring.

### Consequences

- Simpler code than either NATS or pg NOTIFY design — no subscription
  goroutine, no channel management, no message handler.
- Zero coordination bugs possible — filesystem semantics are
  well-understood.
- Multi-instance concurrent writes are safe: atomic rename means
  readers see either full old or full new file, never partial.
- Fingerprint dedupe drops probably 60-80% of Python's backup writes
  during a goal burst — most searches don't rotate cookies.
- All Twitter service instances self-recover on their own next auth
  check when cookies get refreshed (whether by VNC or by another
  instance's search); no coordination needed.

### What the earlier "pg NOTIFY" bit of the decision below still means

The broader architectural principle — NATS is workspace-level infra
for inter-project pub/sub only, intra-project reactive triggers use
pg NOTIFY when needed — still applies. Just, Twitter fleet auth
coordination turned out not to need any reactive trigger at all.
pg NOTIFY is still available for future intra-project use cases that
genuinely need it; it's just not needed here.

---

## 2026-07-21 — NATS scope: inter-project only; pg NOTIFY for intra-project pub/sub

**Supersedes (partial):** T/b portion of `docs/design/proposals/twitter-port.md`
that specified `twitter.auth_expired` / `twitter.reauthed` NATS events for
Twitter fleet auth coordination. Also flags a follow-up revision to
`docs/design/proposals/api-contract.md`'s SSE-bridge section.

### Context

Conversation triggered by the T/b Twitter auth work: the signed-off
`twitter-port.md` proposal used NATS subjects (`twitter.auth_expired`,
`twitter.reauthed`) to coordinate cookie state across a headless fleet.
Discussing whether to implement it exposed a broader architectural
question — the twitter-port design was treating NATS as an
intra-project pub/sub mechanism, but the workspace's original design
intent (per `~/workspace/nats/`) is that NATS is workspace-level infra
for **inter-project** communication (found-footy ↔ vedanta-systems,
found-footy ↔ joi control plane, found-footy ↔ nexus, plus optional
Grafana / webhook-dispatcher subscribers). Same pattern nexus itself
uses per `~/workspace/nexus/docs/cluster/control-plane.md`.

The O3/a-c commits published `event.*` and `fixture.*` to NATS — those
publishes ARE inter-project (vedanta-systems is the intended
subscriber for browser SSE fan-out). That usage is correct and stays.

The mistake was scoping twitter fleet auth coordination — a
genuinely intra-project concern — as another NATS use.

### Decision

**NATS is reserved for inter-project communication.** Concretely, in
found-footy:

- Publishing domain events (`event.*`, `fixture.*`) to workspace NATS
  for consumption by vedanta-systems, Grafana, webhook-dispatcher,
  and future subscribers → **NATS** (unchanged from O3)
- Talking to joi's control plane (once it's deployed on luv) for LLM
  inference requests → **NATS request/reply** (replacing today's
  direct HTTP to `llama-*.joi`, matching workspace principle even
  though nexus/joi control planes accept both HTTP and NATS ingress)
- Talking to nexus's control plane → **NATS request/reply** for
  consistency with the same principle
- Twitter fleet auth coordination (intra-project) → **pg NOTIFY**
  (uses existing pg dependency, no cross-project coupling)
- API's live-updates fan-out to browsers → **vedanta-systems'
  responsibility** (subscribe to workspace NATS, do SSE from there);
  found-footy's API surface stays focused on request/response
  (historical query GETs + webhook subscription management)

### Consequences

- twitter-port.md T/b section updated: replace NATS auth
  coordination with pg NOTIFY on channel `twitter_auth`. Same
  behavior (all instances notified when auth state changes),
  different transport, no cross-project involvement.
- roadmap.md Week 1 T/b task updated same way.
- api-contract.md's SSE bridge description needs revision to reflect
  vedanta-systems being the SSE-to-browsers layer, not found-footy.
  DEFERRED — no code shipped today depends on this; revise when API
  surface (Phase A) actually starts implementation.
- The `internal/infra/nats/` adapter stays exactly as-is — it's
  found-footy's client to workspace NATS, valid for cross-project
  publishes. No code churn from this decision.
- Future joi/nexus control-plane work: when those come online, the
  `internal/infra/llm/` adapter's transport swaps from direct HTTP
  to NATS request/reply. `LLMClient` interface stays the same;
  concurrency semaphore moves out of found-footy code into the
  control plane. No urgency — direct HTTP works fine until the
  control planes are deployed.

### Note on pg NOTIFY as the intra-project bus

`pg LISTEN`/`NOTIFY` uses the existing pg dependency (no new server,
no new client library, no new failure mode). Payload limit is 8 KB
per message which is fine for coordination signals (Twitter auth
state, config reload triggers, etc. — not for large event payloads
which stay on the NATS bus via O3's dual-write pattern). If found-
footy ever needs intra-project pub/sub for something that outgrows
8 KB, we revisit; for now pg NOTIFY handles all foreseeable use.

---

## 2026-07-21 — Alias entity resolution: Wikipedia CirrusSearch replaces `wbsearchentities`

**Supersedes (partial):** the LOOKUP section of the 2026-07-19 decision
below. Selection pipeline unchanged.

**Design ref:** [`./rebuild/proposals/alias-entity-resolution.md`](design/proposals/alias-entity-resolution.md).

### Context

The Wikidata-first pipeline shipped in commits `482d4ed` (baseline) →
`f5f11db` (api-football enrichment) → `0560b12` (P31 batch verify)
resolved 37 of 38 tracked-league teams on the 2026-04-26 dev roster.
The persistent miss — Nice (target Q185163 OGC Nice) — was NOT solvable
by any downstream filter: `wbsearchentities` never returned Q185163 for
any of our 9 fuzzy variants (`Nice FC`, `Nice football club`, `Nice
France football`, etc.) in any language. Root cause: `wbsearchentities`
is a **prefix-oriented label + alias index**. Wikidata's canonical
label for OGC Nice is `OGC Nice` (in every language sampled), and our
search inputs contain no prefix that hits it.

This is a general class — sponsor-prefixed clubs (Red Bull Salzburg vs
Salzburg), organization-prefixed clubs (OGC Nice vs Nice), and
non-Latin native scripts (Al-Ahly = الأهلي) all fail the same way. It
gets strictly worse as coverage expands beyond the top-5 European
leagues.

The proposal doc considered three approaches (A/C/D). A (multi-language
`wbsearchentities`) was empirically tested and rejected — it's the same
prefix index, just in a different language. C (Wikipedia full-text
search + Wikidata cross-reference via `pageprops.wikibase_item`) tested
successfully and is what shipped. D (Twitter-usage bootstrap) is
shelved for post-MVP.

### Decision

**Wikipedia is the entity resolver; Wikidata is the alias source.**

Pipeline (per cache-miss team):

1. `apifootball.GetTeamProfile(teamID)` — enrichment (venue.city,
   authoritative country/national, team.code). One HTTP.
2. `wikipedia.SearchAndResolve(query, opts)` — CirrusSearch full-text
   over article bodies with template:
   - Clubs: `{name} {country} football club`
   - Nationals: `{country} men's national football team`
   Returns hits with each article's Wikidata QID extracted from
   `pageprops.wikibase_item`. ONE HTTP round-trip via
   `list=search + generator=search + prop=pageprops` composed.
3. `wikidata.BatchGetP31([qids])` — ONE SPARQL query, structural type
   verification. Accept-set + reject-set unchanged from the 2026-07-20
   commit `0560b12`.
4. First Wikipedia-ranked survivor of P31 verify wins.
5. `wikidata.GetEntity(qid)` — canonical labels + multi-lang aliases +
   P1449 nicknames + P17 country link for the SELECT phase (unchanged).

Fallback on SPARQL failure: take Wikipedia's top hit unconditionally
rather than cascade to NoMatch across the whole roster. Wikipedia's
BM25 + field-boosted ranking is generally right; graceful degradation
beats a vendor-blip outage.

### Why Wikipedia's full-text search works when `wbsearchentities` doesn't

- CirrusSearch (ElasticSearch under the hood) indexes **article body
  text**, not just short curated labels. Every paraphrase, historical
  name, sponsor-prefix variant that Wikipedia editors have written into
  intro paragraphs and infoboxes is retrievable.
- BM25 scoring favors articles where the query terms are dense and
  topically central. An OGC Nice article that mentions "Nice" 100 times
  in a football context outranks the "Nice (city)" article for the
  query `Nice France football club` — even though the city article
  ALSO contains "Nice" and "France".
- Wikipedia is effectively a crowd-sourced redirect table from ANY
  plausible mention of an entity to its canonical article. Wikidata is
  a downstream structured extract that loses that paraphrase density.

### Consequences

- Interface diet: `alias.WikidataFetcher` shrunk from 3 methods to 2
  (`GetEntity` + `BatchGetP31`; `SearchEntities` removed).
- New `alias.WikipediaResolver` interface + `internal/infra/wikipedia/`
  adapter (roughly the same shape as the wikidata adapter, one
  method: `SearchAndResolve`).
- Deleted from the domain: 9 club search variants, 3 national
  variants, description-quality scoring (3 constants + `isFootball`
  text heuristic + fallback function), the `CountryVariations`
  machinery for description-scoring, description skip keywords, label
  skip suffixes. Net ~440 lines removed.
- Two hardcoded query templates replace 12 wbsearchentities variants
  + related scoring hardcoding. Reduction, not addition.
- HTTP volume per team lifetime: down from ~12 to 4-5.
- Nice resolves to Q185163 with rich alias set
  `{cote, gymnaste, nice, niza, nizza, ogc, ogcn, olympique}`. Inter
  now includes `milan` as an alias (Wikipedia article title is "Inter
  Milan") — an alias the earlier pipeline was dropping.

### Note on `docs/design/rebuild-plan.md` (the design bible)

Plan §9 `internal/infra/wikidata` still describes a `SearchEntities`
method on the client interface. That reflects the pre-2026-07-21
design; the shipped interface is `GetEntity + BatchGetP31 +
SearchEntities` (the last remains on the client for future callers but
is no longer on the narrow `alias.WikidataFetcher` interface). No
separate rebuild-plan §9 subsection for `internal/infra/wikipedia`
exists yet — treat this decision + `proposals/alias-entity-resolution.md`
as the authoritative reference until the plan gets its next scheduled
sweep.

---

## 2026-07-19 — Team alias pipeline: deterministic Wikidata, no LLM

**Supersedes:** Python's `archive/src/activities/rag.py` full RAG pipeline (LLM in three roles).

**Design ref:** [`./rebuild/proposals/team-aliases.md`](design/proposals/team-aliases.md) — full pipeline + empirical basis + implementation plan.

### Context

Python's alias pipeline used the LLM for three different jobs conflated into one prompt:

1. **Selection** — pick 3-5 best words from Wikidata's raw alias list.
2. **Derivation** — Argentina → "Argentine", "Argentinian".
3. **Gap-fill / world-knowledge augmentation** — supply nicknames absent from Wikidata (Atlético's Colchoneros/Rojiblancos, Argentina's Scaloneta, Brazil's Canarinho/Verdeamarela).

Working through the redesign 2026-07-18 → 2026-07-19, an empirical evaluation (session-scoped `alias-eval.md` produced by a research agent) tested 10 deterministic algorithm variants against 15 clubs + 10 nationals with a hand-curated gold standard. Key findings:

- **Multilingual Wikidata (11 Latin-script langs) + P1449 nickname property covers roles 1 and 2 well** — matches Python's 99.9% lookup hit rate with better recall on distinctive nicknames (Barça, Les Bleus, Albiceleste, Seleção — all recoverable).
- **P1549 demonym property replaces Python's LLM-derived nationality adjectives**. Both single-form and dual-form recovered: Argentina → Argentine + Argentinian, UK → British + Briton, Spain → Spanish + Spaniard, all in Wikidata's country entities.
- **Role 3 (gap-fill) has NO deterministic replacement**. Colchoneros, Scaloneta, Canarinho are simply absent from Wikidata. No algorithm variant recovers them.

Options for role 3: (a) accept the tail loss, (b) hardcode fallback (YAML in repo or supplemental pg table), (c) narrow LLM gap-fill call cached 30d. Path traversed briefly through (c) mid-session, then rejected — the deterministic pipeline captures the essential aliases (canonical name variants + P1549 demonyms + P1449 nicknames + cross-language distinctive terms), and the tail (Scaloneta, Colchoneros, Palanganas) represents ~5% of legit tweet-relevant nicknames. Tweets using those niche nicknames also almost always contain a dominant term (Argentina, Atlético, Sevilla) that our OR-query catches. User's preference: avoid LLM complexity unless empirically needed.

### Decision

**Deterministic Wikidata pipeline, no LLM in the alias path. 30-day pg cache.**

**Selection algorithm** — split by team type, both branches share word-processing:

- **Word-processing**: NFD normalize, strip Unicode combining marks (diacritics), `ß → ss` preprocessing, split on space/dash, drop punctuation, lowercase, drop ≤2 chars, drop pure-digit, drop CamelCase concatenations. Multilingual skip-list of pure organizational descriptors (`fc/ac/sc/cf/football/futbol/futebol/calcio/fussball/nazionale/nationalmannschaft/selection/national/team/equipe/reprezentacja/nogometna/voetbal/etc.` + articles). **Team-identifying words that look generic are explicitly NOT skipped**: `united, city, athletic, sporting, real, rangers, rovers, town, wanderers, borussia, juventus, seleção, seleccion, elftal, mannschaft` — these DO distinguish teams when combined with context (Manchester United, Real Madrid, Sporting CP, Netherlands, Germany, etc.), and Python's LLM was known to over-filter them. Corrected mid-design after the eval agent's initial skip-list incorrectly included `sporting` (per proposal doc, 2026-07-19).

- **Clubs** (V5-shaped): aliases from `labels.<lang>` + `aliases.<lang>` in `en/es/fr/it/pt/de/ca/gl/nl/pl/ro` (11-language Latin-script subset) + P1449 nickname property values (language-agnostic). Keep rule: word is kept if it's canonical team name from API-Football, or in P1449, or appears in **≥2 distinct languages** after normalization. Empirically ≥2 beats ≥3 by 0.03 F1 (≥3 drops legit acronyms LFC/CFC/MCFC). Additional venue-city skip: drop the venue city if it's not a substring of the canonical team name (fixes "London" for Arsenal; preserves "Liverpool" for Liverpool F.C.).

- **Nationals** (V8-shaped): same word-processing + language subset + keep rule. Plus P1549 demonym extraction: query the linked country entity (via P17), extract demonym forms restricted to the same 11-language subset (drops Bulgarian/Hebrew/Tamil noise while preserving Argentine + Argentinian, Spanish + Spaniard). No venue-city concept for nationals.

- **No top-N cap** — advanced Twitter search OR-syntax handles unlimited alias count in one query, bounded only by ~500-char query limit (never approached in practice, ~25 aliases per team typical).

**Inputs from API-Football that participate in the pipeline:**

- `team.name` — canonical name. Kept as a phrase for exact-match, word-split into candidate aliases (each word through the skip-list / keep-rule).
- `team.code` — 3-letter FIFA/UEFA code (NEW/LIV/BAR/etc.). Added as a candidate alias; passes skip-list if distinctive, filtered if it collides with a generic English word.
- `team.country` — used for lookup-phase disambiguation.
- `venue.city` — used for the venue-city skip rule in selection.
- `team.national` — determines which branch (V5 clubs vs V8 nationals) runs.

**Caching model (`team_aliases` pg table):**

Row per team_id with columns: `aliases text[]`, `wikidata_qid text`, `resolved_at timestamptz`, `canonical_name text`. 30-day TTL on `resolved_at`. On refresh: if `wikidata_qid` is populated (cache hit for lookup phase), skip fuzzy search and re-run selection directly. Only genuinely new teams (never seen before) hit the expensive lookup path. QIDs are effectively permanent (football-club entities on Wikidata don't get deleted), so lookup is one-time-per-team-ever.

### Coverage acknowledgment

Deterministic pipeline captures ~90-95% of legit tweet-relevant aliases. Missing tail (~5-10%) includes: Colchoneros/Rojiblancos (Atlético), Palanganas/Nervionenses (Sevilla), Scaloneta (Argentina, post-2022 nickname), Canarinho/Verde-amarela (Brazil), ManU/ManCity (concatenated shorthands), Madridistas (Real Madrid), Tricolores (France). All are legitimate but niche — dominant tweets about these teams also contain canonical or common-nickname terms that our OR-query catches. LLM gap-fill can be added later as targeted enhancement if prod hit-rate suggests a specific team is underperforming; not part of the initial ship.

### Not decided

- Lookup (name → Wikidata QID) pipeline is task #133 — porting Python's 7-variant fuzzy `wbsearchentities` + description-scoring stack as-is, replacing Python's LLM-derived country variations with Wikidata P1549 + P1448 lookups.

## 2026-07-18 — Video share ranking derived at read time, no stored rank column

**Supersedes:** `rebuild-plan.md` §5 W5 (AssetWorkflow includes rank recalc activity) and `rebuild/proposals/video-dedup.md` V/g (rank recalc during upload). Python's `upload_workflow.recalculate_video_ranks` pattern is NOT ported.

**Design ref:** design conversation captured mid-session 2026-07-18 while discussing AssetWorkflow completion mechanics.

### Context

Python stores a `rank` column on each video share and recalculates it inside `upload_workflow._process_batch` via `store.recalculate_video_ranks` (see [python-functional-spec.md upload spec §8](design/python-functional-spec.md)). The pattern has two known production issues:

1. **`rank=0` bug observed in prod.** `save_video_objects` writes the video record with default `rank=0`. `recalculate_video_ranks` is treated as non-fatal, max-2-retries, log-and-move-on (upload-spec §8). If both retries fail, ranks stay at default silently. User has confirmed seeing rank=0 videos in the current prod frontend.
2. **Concurrent-batch race window.** Between a video insert in one batch and the rank recalc that follows, another batch's insert can land, showing rank=0 on the frontend until the next batch's recalc fires. On a Champions League night with 10+ concurrent event pipelines, this window is real.

Both are architectural — no amount of "retry harder" fixes them without introducing more complex transaction reasoning. The root cause is that rank is a cached-derivation-of-underlying-columns treated as authoritative, with the cache-invalidation logic (recalc after each batch) racing against the mutation logic (insert new share).

### Decision

**Rank is derived at read time via SQL window function, not stored.**

Concretely:

- `video_shares` table has NO `rank` column.
- API queries for shares return them already ranked, e.g.:
  ```sql
  SELECT
    s.*,
    ROW_NUMBER() OVER (
      PARTITION BY s.event_id
      ORDER BY a.verified DESC, popularity DESC, a.quality_score DESC NULLS LAST
    ) AS rank
  FROM video_shares s
  JOIN video_assets a ON a.id = s.video_asset_id
  LEFT JOIN LATERAL (
    SELECT COUNT(*) AS popularity FROM video_shares WHERE video_asset_id = a.id
  ) p ON true
  WHERE s.event_id = $1 AND NOT s.removed
  ORDER BY rank
  ```
- No rank-recalc activity in any workflow.
- No `event.rank_recalculated` NATS emit during normal flow. Any share change (insert / remove / migrate via replace+absorb) implicitly means ranks may have changed; SSE consumers re-fetching on the invalidation event see fresh ranks automatically.
- The shipped `KindEventRankRecalculated` in `internal/infra/event/subjects.go` is kept in the enum for future use (e.g., algorithm-change or quality-score backfill scenarios that don't produce share changes) but is not emitted during normal workflow flow.

### Rationale

- **`rank=0` becomes impossible** — no column to be stale, no "just inserted, not yet ranked" intermediate state.
- **No race conditions** — every read sees consistent, criteria-derived ordering.
- **Ranking criteria can change without migrations.** Adding a "recency" bonus, tuning weights, adding a factor → change the ORDER BY expression. No column rewrite, no historical data to migrate, no in-flight recalc storm.
- **Simpler code** — one activity deleted (rank recalc), one workflow signal removed, one NATS emit removed, one column not added.
- **Cheap reads at our scale** — video_shares per event are dozens at most; a window function ranking is trivial compute compared to any HTTP round trip.
- **Correct-by-construction** — no "cache invalidation" question exists.

### Consequences

- `video_shares.rank` column: **not added to schema**. If it accidentally lands in a migration, remove it.
- `AssetWorkflow`: no rank-recalc activity, no `event.rank_recalculated` emit in normal flow, per-batch drain completion focuses only on dedup + upload + share insert (see decisions.md 2026-07-16 downstream spawn rule + queue-drain pattern discussed 2026-07-18).
- API query surface (`cmd/api`): use window-function ORDER BY in the "shares for event X" query.
- SSE consumers: re-fetch shares on any `event.*` / `fixture.*` invalidate event they care about — ranks always fresh.
- Preserved as-is: `verified` classification, popularity via `COUNT(video_shares)`, quality score from vision call #2. These are the ORDER BY inputs; how they're computed doesn't change.
- Preserved conceptually: the frontend's "top clip gets a badge" pattern still works — rank = 1 comes from the same window function that would have populated the column. UI code changes zero.
- Rejected alternatives:
  - Per-video recalc (10× redundant work per batch)
  - Per-batch recalc, harden the retry (still races)
  - Post-drain single recalc at workflow completion (5–10 min of stale ranks during a goal)
  - Debounced separate recalc workflow signaled on every share change (works but adds moving parts for a problem the derived-at-read pattern makes disappear entirely)

---

## 2026-07-16 — Downstream workflow spawn via Temporal-direct + register-on-flip (chain, not NATS)

**Supersedes:** the [O3/c NATS-triggered spawn design](design/proposals/discovery.md) inside `rebuild/proposals/discovery.md`. NATS-as-event-bus (2026-07-01) remains in force for external fan-out; it is no longer the trigger path for internal named workflows.

**Design ref:** [`rebuild/proposals/discovery.md`](design/proposals/discovery.md), revised in the same commit.

### Context

The plan-of-record for O3 spawned DiscoveryWorkflow via a NATS
subscriber goroutine reading `event.stable`. Monitor's
`ReconcileFixture` would flip `events.downstream_triggered=true` and
publish to NATS; the subscriber would then spawn Discovery. This left
a race window between the flag flip and Discovery's row appearing in
`event_downstream_workflows`:

1. `ReconcileFixture` flips `downstream_triggered=true`, publishes
   `event.stable`.
2. Same activity's Step 6 runs `FixtureReadyToComplete`. Sees Terminal
   ✓ + counter ≥ 3 ✓ + all events settled ✓ + no in-flight downstream
   rows (Discovery hasn't spawned yet) ✓ → transitions fixture to
   `completed`.
3. Milliseconds later, NATS subscriber picks up `event.stable`, spawns
   DiscoveryWorkflow. Discovery's first activity registers its
   checklist row against a fixture already in `completed` state.
   Discovery + downstream continue writing to `events`, `video_shares`,
   `video_assets` while the fixture publicly reads as done. The
   completion state is a lie for the duration of the downstream chain.

Sub-second window in practice. Football produces the exact
last-minute-goal-then-full-time timeline that hits it — reliably.

Deeper design issue: NATS-triggered spawn splits ownership of a single
logical state change ("this event now has Discovery pending") across
two processes. Monitor writes the flag; the subscriber writes the row.
Atomicity across that boundary is unreachable without either a
two-cycle rule (adds 30s to happy-path completion + still splits
ownership) or a synthetic pending-marker (equivalent to just doing the
insert on the Monitor side).

### Decision

**Spawn rule for every downstream workflow in the chain:**

The code that decides to spawn a downstream workflow does three things
in a single activity, with pg-transaction atomicity for the state
changes and idempotent spawn semantics:

1. Flip the source-of-truth state change (e.g. `events.downstream_triggered=true`) — pg tx.
2. INSERT the `event_downstream_workflows` row for the workflow being spawned — same pg tx, commit. Deterministic `workflow_id` (e.g. `discovery-{event_id}`).
3. Call `temporalClient.ExecuteWorkflow` with that same deterministic ID and `WorkflowIDReusePolicy: RejectDuplicate`. Swallow `WorkflowExecutionAlreadyStarted` — expected on activity retry.

Applied per pipeline stage — **chain, not fan-out coordinator:**

- **Monitor → Discovery.** `ReconcileFixture` flips flag, inserts Discovery row, spawns Discovery.
- **Discovery → Video.** A Discovery activity that identifies a candidate clip inserts Video's row, spawns Video.
- **Video → Asset.** A Video activity that finalizes clip validation inserts Asset's row, spawns Asset.

Each stage owns its checklist row + successor spawn. Fixture
completion check unchanged: "any rows for this fixture's events with
`completed_at IS NULL`?" The checklist is self-populating as work
fans down the chain.

**NATS scope narrowed:**

NATS emissions for `event.stable`, `event.detected`, `event.removed`,
`fixture.activated`, `fixture.completed`, `event.rank_recalculated`
still happen — but only for external fan-out consumers:

- SSE bridge in `cmd/api` (browser real-time)
- Webhook delivery worker (JetStream durable consumer)
- Any future non-workflow consumer we don't know about yet

**NATS is not the trigger path for named internal workflows.** When
producer and consumer are both Temporal workflows inside our
namespace, transport is Temporal. NATS earns its keep across process
boundaries where consumers are dynamic, unknown, or non-workflow —
cross-project, cross-node, browser, or user-registered endpoints.

pg `event_log` remains the source of truth; NATS remains best-effort
with pg replay for reconnecting subscribers.

### Rationale

- **Atomicity for free.** Checklist row exists before the flipping
  activity returns → the completion check in the same or next cycle
  correctly sees "downstream pending." No two-cycle rule, no synthetic
  pending marker.
- **Right tool for the job.** Temporal is built for named workflow
  orchestration with state tracking, retry, and an observable
  execution graph. NATS is built for pub/sub fan-out to dynamic or
  unknown consumers. Discovery is a known named consumer, always;
  routing that through a broker adds a hop and a delivery model
  without earning it.
- **NATS keeps its purpose.** Fan-out to consumers whose identity the
  producer shouldn't have to know (SSE frontends, webhook endpoints,
  external services on other nodes) is exactly pub/sub's sweet spot.
  Intra-Temporal workflow orchestration is not.
- **Observability preserved.** Temporal's workflow graph shows
  Monitor → Discovery → Video → Asset causal linkage. Under the
  NATS-triggered design that linkage was broken — Discovery appeared
  to be "started by the NATS subscriber."
- **One delivery model.** Temporal's activity + workflow retry
  semantics cover the whole orchestration path. No JetStream consumer
  position + ack/nack + dead-letter reasoning layered on top.
- **Chain over coordinator.** The Discovery → Video → Asset
  dependency is real data flow (Video needs Discovery's candidate
  URLs; Asset needs Video's validated clip). Making each stage the
  spawner of its successor puts the dependency where it lives. A
  per-event coordinator would abstract the dependency behind
  indirection that earns no keep. Rejected.

### Consequences

- **[`rebuild/proposals/discovery.md`](design/proposals/discovery.md)** revised:
  - O3/a — NATS event composer stays. Still needed for external emit + audit trail.
  - O3/b — Monitor emits to NATS for external consumers **and** inserts Discovery row + spawns Discovery via Temporal client in the same activity as the flag flip.
  - O3/c — DiscoveryWorkflow skeleton stays; the "NATS subscriber goroutine" section removed.
- **`internal/activity/monitor/activities.go`** `ReconcileFixture` gains a paired step alongside the flag flip: row insert + spawn. Deps grow a `DownstreamSpawner` interface (thin wrapper over the Temporal client, so activities stay unit-testable with a fake).
- **Deterministic workflow-ID convention** documented at the domain layer: `discovery-{event_id}`, `video-{event_id}-{share_id}`, `asset-{share_id}`. `RejectDuplicate` policy enforces uniqueness server-side.
- **Activity retry semantics** preserved: `event_downstream_workflows` insert uses `ON CONFLICT DO NOTHING`; Temporal spawn returns `WorkflowExecutionAlreadyStarted` on retry which we swallow as success.
- **Race from the completion-contract era eliminated.** `FixtureReadyToComplete` needs no two-cycle safety net.
- **Rejected alternatives:**
  - NATS-triggered spawn + two-cycle completion rule — adds 30s to happy-path completion; still splits ownership.
  - NATS-triggered spawn + Monitor pre-inserts a pending marker — equivalent in structure to this decision, plus a useless broker hop.
  - Per-event coordinator workflow — adds a layer; the pipeline is a chain, not a fan-out.
  - Singleton lifecycle manager — bottleneck + event history bloat.

---

## 2026-07-11 — Fixture completion contract via pluggable per-event workflow checklist

**Design ref:** [`rebuild/proposals/completion-contract.md`](design/proposals/completion-contract.md)

### Context

Plan §8 speced fixture completion as "API status Terminal + all
non-removed events have `_monitor_complete AND _download_complete`."
Python's implementation checked these per-event booleans plus a 3-poll
counter. Two things surfaced during design:

1. **Extensibility.** As we add downstream workflows (sentiment
   analysis, text summarization, future post-MVP things), each new
   type would need its own boolean on `events` and its own branch in
   the completion check. Anti-pluggability.
2. **Correctness.** `downstream_triggered=true` only means we STARTED
   downstream. It doesn't mean downstream FINISHED. If we move the
   fixture to `completed` state while a DownloadWorkflow or
   UploadWorkflow is still writing to `events` / `video_shares` /
   `video_assets`, those writes land on a "frozen" fixture. Bad.

### Decision

Pluggable per-event workflow checklist via a new table
`event_downstream_workflows`. Every downstream workflow (Discovery,
each DownloadWorkflow, UploadWorkflow, future workflows) follows a
uniform register/complete protocol:

- **On start:** INSERT (event_id, workflow_type, workflow_id) with
  `completed_at=NULL`. `ON CONFLICT DO NOTHING` for Temporal replay
  idempotency.
- **On exit** (success or failure): UPDATE the row with
  `completed_at=NOW()`, `outcome_class`, optional `metadata` JSONB.

Fixture completion check reduces to:

1. `api_status_short` in the Terminal set (`ft`,`aet`,`pen`,`canc`,
   `abd`,`wo`,`awd`).
2. `completion_counter >= 3` OR `home_winner IS NOT NULL` OR
   `away_winner IS NOT NULL` (fast-path when vendor sets winner flags
   before the 3-poll debounce completes).
3. `NOT EXISTS` any event where `removed=false AND
   downstream_triggered=false` (no mid-debounce events — events must
   have settled to either VAR'd or stable).
4. `NOT EXISTS` any row in `event_downstream_workflows` where
   `completed_at IS NULL` for any event in the fixture.

All in one SQL query via `FixtureRepo.FixtureReadyToComplete(fixtureID)`.
Called from `ReconcileFixture` at the end of its per-cycle work; if
ready, transitions state to `completed` in the same activity.

### Rationale

- **Pluggability without schema migration.** New downstream workflow
  type lands → picks a new `workflow_type` string. Completion check
  code doesn't change. Just more rows to consider.
- **Introspectability.** "Which workflow is holding this fixture
  from completing?" → `SELECT workflow_type, workflow_id, started_at
  FROM event_downstream_workflows WHERE event_id=$1 AND completed_at
  IS NULL`. Reference-counter alternatives can't answer this.
- **Race-free polling.** ActivePoll's per-fixture reconcile does the
  check. Workflows just update their row. No signal wiring, no
  "am I the last?" logic.
- **Winner fast-path preserved.** Python's Python's fast-path
  (move to completed when winner data appears, before the 3-poll
  counter finishes) matches natural user expectations — the fixture
  is "over" as soon as the vendor commits to a result.
- **Completion counter symmetric with event debounce.** Same
  0-to-3 cap pattern used for event presence votes. Familiar
  invariant.

### Consequences

- Schema additions: `event_downstream_workflows` table,
  `fixtures.completion_counter`, `fixtures.home_winner`,
  `fixtures.away_winner`.
- `Fixture.UpdateFromPoll` now maintains `completion_counter`
  (increment on Terminal, reset on non-Terminal).
- `Fixture.UpdateWinners(home, away)` added — separate from
  UpdateFromPoll since winner flags don't appear in every poll.
- `Fixture.HasDecidedWinner() bool` — used by the completion check.
- `ReconcileFixture` runs the completion check at the end; sets
  `out.Completed = true` and transitions state on ready.
- **Pre-cutover behavior:** `event_downstream_workflows` is empty
  (O3-O5 unbuilt). Fixtures with no non-removed events complete
  correctly at Terminal + counter. Fixtures with stabilized events
  ALSO complete, prematurely, because no downstream workflows exist
  to hold them. This is fine pre-cutover (no user-facing consequence);
  as soon as O3-O5 land and start registering rows, the completion
  check auto-widens.
- **Coexistence:** `event_download_workflows` (the 10-download
  registration threshold table) stays for now. When O4 lands, its
  downloads will register in `event_downstream_workflows` for
  completion tracking and continue using `event_download_workflows`
  for the "10 attempts" counting. Consolidation is a future
  optimization.
- Rejected alternatives (see completion-contract.md § Rejected):
  reference counter (no introspection), JSONB blob (write cost),
  per-workflow-type tables (anti-pluggability), event-driven
  completion trigger (race-prone).

---

## 2026-07-11 — Split MonitorWorkflow into ActivePollWorkflow + StagingPollWorkflow

**Supersedes:** rebuild-plan.md §5 W2 (single MonitorWorkflow spec) and
the 2026-07-07 staging-poll design (bucket-suppression inside a single
MonitorWorkflow at 30s cadence).

### Context

Plan §5 W2 speced a single MonitorWorkflow that fired every 30s and
internally handled two conceptually different jobs on two different
cadences:

- **Active fixture polling** (30s cadence, batched vendor call, event
  reconciliation) — the hot path for goal detection.
- **Staging fixture polling** (nominally 15-min cadence, batched vendor
  call, vendor-side edge cases like postponement and kickoff correction).

The 2026-07-07 staging-poll entry landed the design for the second job
by suppressing most 30s cycles via 15-min bucket arithmetic
(`hour*4 + minute//15` derived from `last_polled_at`) so the API call
only fired on 15-min-aligned boundaries.

Implementation of that design surfaced the bucket math as a workaround
for cramming two jobs into one workflow. The `StagingPollInterval`
config field wasn't actually consumed by the bucket math — the 15-min
bucket count was hardcoded, meaning changing the config knob did
nothing. The config field lied about what it controlled.

### Decision

Split into two workflows on independent Temporal Schedules:

**`ActivePollWorkflow`** — Temporal Schedule `active-poll-scheduled`,
IntervalSpec `Every: 30s` (source: `Workflows.ActiveFixturePollInterval`).
Steps:

1. `ActivateUpcoming` (DB-only, renamed from `PreActivateUpcoming` —
   the "Pre" prefix was misleading; this IS the standard activation)
2. `ListActiveFixtureIDs`
3. `FetchLiveFixtures`
4. `ReconcileFixture` (parallel per fixture)

**`StagingPollWorkflow`** — Temporal Schedule `staging-poll-scheduled`,
cron `*/15 * * * *` (source: `Workflows.StagingPollCron`, runtime-tunable
via `temporal schedule update` without redeploy). Steps:

1. `PollStagingFixtures` — vendor edge cases only. Two activation
   sub-paths inside: Live()-emergency (Path 3b) and kickoff-corrected
   (Path 3a). No bucket math — the schedule owns cadence; the
   activity polls all staging fixtures on each tick.

### Rationale

- **Bucket math was a workaround.** One workflow trying to do work
  on two cadences forces suppression logic to skip most cycles.
  Removing the cram removes the workaround.
- **Failure isolation.** ActivateUpcoming (P0 — missing this loses
  matches) now inherits the resilient 30s workflow's failure profile
  instead of being coupled to the P2 staging poll (P2 because vendor
  edge cases can be re-tried next tick without user-visible impact).
  A broken StagingPollWorkflow should be a P2 incident; a broken
  activation would be P0 (missing matches). Colocating them meant
  one incident.
- **Runtime tunability.** Each schedule is independently
  `temporal schedule update`-able. Operations can pause staging
  poll during a vendor incident, tighten cadence during a major
  tournament, etc., without a code redeploy.
- **Config becomes honest.** `StagingPollInterval` config field
  deleted (it was a lie — 15 min was hardcoded in bucket math).
  `StagingPollCron` string field added, used to create the schedule
  at worker startup. Runtime updates via Temporal admin override the
  code-driven cron on subsequent restarts (Create is idempotent —
  ErrScheduleAlreadyRunning → success).
- **`ActivationWindow` decoupled from staging cadence.** Was
  `StagingPollInterval × ActivationMultiplier`. Now a direct config
  field, default tightened from 30m to 5m. Reasoning: 30m was sized
  as safety margin over 15-min staging cadence; with ActivateUpcoming
  running every 30s at the DB layer, the margin required is drift
  buffer against vendor kickoff timestamp variance (~5 min covers
  real-world early/late starts) rather than cadence coverage. Matches
  starting >5 min earlier than scheduled are essentially impossible
  in professional football (TV constraints); StagingPoll's Live()
  emergency path (Path 3b) covers the impossible-case backstop.

### Consequences

- Two workflows registered in `cmd/worker/main.go` instead of one.
- Two Temporal Schedule create calls (both idempotent — existing
  schedules survive worker restart).
- `fixture.Repo.ListStagingForBucketPoll` deleted (added earlier
  same day, before this decision). `PollStagingFixtures` now uses
  the existing `ListByState(StateStaging)`.
- Emergency-activated fixtures (Path 3b) land in `active` state at
  StagingPoll tick T. First reconcile happens at the next
  ActivePoll tick — average delay ~15s, worst case 30s. Not a
  correctness regression; emergency-activation means we were
  already several minutes behind by definition, and the 3-cycle
  debounce (90s) downstream dominates the pipeline latency budget.
- Config: `StagingPollInterval` deleted, `ActivationMultiplier`
  deleted, `ActivationWindow()` method deleted; `StagingPollCron`
  added, `ActivationWindow` becomes a direct field, default `5m`.
- Test harness's scenario `workflow: "MonitorWorkflow"` accepted as
  legacy alias for `ActivePollWorkflow`. Scenarios exercise event
  reconcile, which lives in the active path.
- `PreActivateUpcoming` renamed to `ActivateUpcoming` (input/output
  types renamed similarly). "Pre" prefix was misleading — it IS the
  standard activation, not a preliminary step.

Working discipline followed: read plan §5 W2 + Python spec §4 before
touching code, surfaced the split as a divergence before implementing,
updated `orchestration.md` + `run-flow.md` + `workflow-audit-2026-07-09.md`
in the same commit.

---

## 2026-07-09 — All-lowercase canonical for enums (uniform internal representation)

Prior enum policy went through two revisions today:
1. "Match vendor doc casing" (initial typed enums commit)
2. "Match vendor real emission" (real-data audit commit)

Both revisions ran into the same problem: **the vendor is
internally inconsistent about casing.** Doc says `"Red card"` but
emission is `"Red Card"`. Doc says `"Subst"` but emission is
`"subst"`. Doc says `"Goal cancelled"` (lowercase 'c') and we've
never observed VAR emission to verify. Every rule of "match X"
produces a mixed-case codebase where the canonical form of each
enum family depends on vendor's particular whims for that family.

User pushed for a uniform rule after seeing the third revision come
up: convert all canonicals to lowercase, preserve vendor's word
separators (spaces) for multi-word values, let Parse handle
case-insensitive normalization at the boundary.

**What shipped**:

- All ~40 enum constants across five families (`APIStatusCode`,
  `APIEventType`, `APIEventDetail`, `APICardComment`, `APIGoalComment`)
  changed to lowercase canonical values. Examples:
    - `StatusNotStarted = "ns"` (was `"NS"`)
    - `EventTypeGoal = "goal"` (was `"Goal"`)
    - `DetailRedCard = "red card"` (was `"Red Card"`)
    - `DetailNormalGoal = "normal goal"` (was `"Normal Goal"`)
    - `CardCommentUnsportsmanlikeConduct = "unsportsmanlike conduct"`
- Domain `event.Type` constants same treatment:
    - `TypeGoal = "goal"`, `TypeMissedPenalty = "missed penalty"`
- `event_type` pg enum recreated with lowercase values:
    - `CREATE TYPE event_type AS ENUM ('goal', 'card', 'subst', 'var', 'missed penalty');`
- Parse functions updated: unknown values now lowercase too, so the
  enum type has a uniform casing invariant regardless of vendor
  emission. Prior behavior preserved unknowns as-is (mixed casing).
- Tests updated: raw string literals `"1H"`, `"NS"`, `"FT"`, `"Goal"`,
  etc. changed to lowercase OR replaced with typed constant refs
  (`apifootball.EventTypeGoal`, etc.) where it made the intent clearer.
- events-shape.md updated with the new policy statement.

**Design choices logged**:

- **Lowercase over doc-canonical or emission-canonical**: fewer
  case-by-case rules to remember. Constants read consistently
  across all families. Vendor inconsistencies still surface if we
  care to log them but don't leak into canonical form.
- **Preserve vendor's word separators (spaces)**: `"missed penalty"`,
  not `"missed_penalty"`. Reason — same value can appear in both
  the domain `event_type` enum (as a domain classification) AND the
  vendor `events.detail` text column (as raw wire value). Using the
  same string keeps DB rows visually simple; column names already
  tell you which layer you're at.
- **Domain enums match vendor format too**: `TypeMissedPenalty = "missed penalty"`
  is the exact same string as `DetailMissedPenalty = "missed penalty"`.
  Semantic layers coexist without collision because they occupy
  different columns.
- **Dev pg volume wiped** to pick up the new event_type enum values.
  Chose wipe-and-reingest over `ALTER TYPE ... RENAME VALUE` because
  the dev DB had only test data (France v Morocco events would
  regenerate on next Ingest cycle anyway).

**Verification**:
- 22 tests pass after the refactor (existing + updated for new casing).
- Dev worker restarted with fresh schema + running.

---

## 2026-07-09 — Real-data enum audit: card/goal comments + Missed Penalty tracking + vendor casing reality

Follow-up to the earlier same-day enum refactor. User pushed back on my
"comments field is free-text" claim from `events-shape.md` and asked me
to verify against real vendor data instead of just the doc. Live audit
against 4 recent WC fixtures + 1 shootout fixture (2026-07-06 through
2026-07-07) surfaced three findings that shifted the design:

**Finding 1: `comments` is NOT free-text — it's a discrete enum per
parent event type.**

Card comments observed (values are exact vendor strings):
- `Foul` (most common on Yellow Card)
- `Argument`
- `Roughing`
- `Unsportsmanlike conduct`
- `Serious foul` (observed on straight red)
- _(null)_

Goal comments observed:
- `Penalty Shootout` (on shootout goals + missed shootout penalties)
- _(null)_ (regular-play goals)

Subst / Var: no non-null comments seen. Extend enums if we ever
observe values.

**Finding 2: vendor emits `"Red Card"` (title case) in live data,
contradicting its own doc which says `"Red card"` (lowercase 'c').**

This isn't a doc typo we can normalize away — vendor is internally
inconsistent about casing across doc + emission across event types
(compare `"Red Card"` observed vs `"Goal cancelled"` doc'd + observed
with lowercase 'c'). Parse handles both via case-insensitive lookup,
but the canonical form is a policy decision.

**Finding 3: `Missed Penalty` occurs in both shootout AND open play.**

Shootout misses have `comments="Penalty Shootout"` — same filter as
shootout goals catches them. Open-play misses (rare — saved
penalties) have `comments=null` and are highlight-worthy but
semantically different from goals. User wants them tracked as a
distinct domain event type so the UI can display them differently.

**What shipped**:

- New `APICardComment` enum with 5 canonical constants +
  ParseAPICardComment. Wire type stays `*string` on APIFixtureEvent
  since comments is context-dependent per parent Type; parsing is
  callable at consumption sites.
- New `APIGoalComment` enum with `GoalCommentPenaltyShootout` +
  ParseAPIGoalComment + `HasPenaltyShootoutComment(string) bool`
  predicate. TrackableEventType now calls the predicate instead of
  a string literal.
- `DetailRedCard` canonical value changed from `"Red card"` to
  `"Red Card"` (matches real vendor emission). Parse still accepts
  both casings so nothing downstream breaks. Log lines now mirror
  the vendor console.
- New domain event type `TypeMissedPenalty`. Added to the
  `event_type` pg enum (`ALTER TYPE event_type ADD VALUE 'MissedPenalty'`
  on running dev DB + updated `schema.sql` for future fresh volumes).
  TrackableEventType returns `(TypeMissedPenalty, true)` when
  `Type=Goal + Detail=Missed Penalty + NOT Penalty Shootout comment`.
- VAR parsing tests added — end-to-end unmarshal path verified for
  both `Goal cancelled` and `Penalty confirmed` details. VAR events
  still skip TrackableEventType (no downstream trigger); user
  wanted the parsing path proven correct so future storage/display
  is unblocked.
- events-shape.md updated with real-data findings + correction of
  the "free-text" claim.

**Design decisions logged**:

- **Canonical case follows real emission, not doc.** Original policy
  (2026-07-09 typed enums entry) said "match vendor doc casing" for
  log-line consistency with the vendor console. But real emission
  diverges from the doc — canonical policy is now "match live
  emission," with Parse's case-insensitive lookup handling both.
- **Wire type stays `*string` for comments.** Considered typing
  APIFixtureEvent.Comments as APIEventComment (or per-type unions),
  but the field's semantics differ across parent Types. Cleaner to
  keep the wire field untyped + expose ParseAPICardComment /
  ParseAPIGoalComment for consumers.
- **Deferred: lowercase-all-canonical follow-up.** User has flagged
  three times now that a policy of "all lowercase internal
  representation" would be cleaner than dancing around vendor's
  casing inconsistencies. Not doing it this commit (bigger churn +
  breaks the current match-real-emission decision), but flagged
  here as a possible future policy shift.
- **Missed Penalty as distinct domain type over TypeGoal + flag.**
  Two options were considered: (a) return TypeGoal + let consumers
  distinguish via Detail field, or (b) introduce TypeMissedPenalty
  as its own domain classification. Chose (b) — cleaner query
  patterns, no risk of consumer forgetting the check, event_type pg
  enum extension is trivial.

**Verification**:
- 22 tests pass (existing + 9 new for CardComment/GoalComment/VAR/
  TrackableEventType classification matrix + updated Red Card casing).
- Dev worker (air hot-reload) still cycling active fixture after
  changes; no runtime errors.

---

## 2026-07-09 — Typed enums for API-Sports wire values (Status + EventType + EventDetail)

Prior state had three vendor-shaped fields as bare `string`:
`APIFixtureStatus.Short` (19 documented values), `APIFixtureEvent.Type`
(4 values), `APIFixtureEvent.Detail` (~11 values). Comparisons happened
at each call site — mostly bare `switch { case "FT": ... }`. Downstream
audits (docs/design/proposals/api-football-audit-2026-07-09.md #1)
caught the class of bug: `TrackableEventType`'s docstring claimed
case-insensitive Type comparison but the switch was `case "Goal":`,
case-sensitive. Works today because vendor sends title-case; silently
breaks if vendor ever normalizes.

**What shipped**:

- New `internal/infra/apifootball/enums.go` with three typed enum
  families, all constants matching vendor doc casing exactly (including
  the vendor's own inconsistencies like `"Red card"` lowercase 'c' and
  `"Goal cancelled"` lowercase 'c').
- `APIStatusCode` — 19 constants for all documented fixture statuses.
- `APIEventType` — 4 constants (Goal, Card, Subst, Var).
- `APIEventDetail` — 9 named constants + a canonical `Substitution`
  that Parse maps `"Substitution 1"`, `"Substitution 2"`, ... to via
  prefix-match (vendor sends per-team indexed substitutions; we don't
  track them so the collapse is inert).
- Three `Parse*` functions accept any casing, normalize via lowercased
  map lookup, return canonical constants OR preserve unknown values
  as-is with `known=false` so callers can log + continue. Vendor may
  add new values without notice; ingest continuing is more important
  than strict validation.
- `UnmarshalJSON` methods on each type call Parse — every value that
  crosses the wire boundary is canonicalized before it hits domain
  code. No more string comparisons downstream.
- `TrackableEventType` signature changed from `(apiType, apiDetail,
  apiComments string)` to `(apifootball.APIEventType,
  apifootball.APIEventDetail, string)`. Switches use typed constants;
  no `strings.ToLower` needed. The docstring lie is gone.
- `domain/fixture.APIStatus.Short` changed from `string` to
  `apifootball.APIStatusCode`. `Live()` / `Terminal()` switches use
  named constants.
- `domain/event.Event.Detail` and `event.New`'s detail parameter both
  changed from `string` to `apifootball.APIEventDetail`.

**Design choices logged**:

- **Canonical form = vendor doc casing** (over all-lowercase). Log
  lines + debug tools mirror what the vendor console shows, easy
  cross-reference during incident triage. Const names have a small
  ergonomic hit (`CardRed = "Red card"` reads weird because vendor
  writes it weird), but consistency with source docs wins.
- **Preserve-unknown over reject-unknown**. Vendor may add new
  fixture statuses (never seen but the doc has no versioning
  guarantee) or event types (VAR arrived in 2020-21 season). If
  unmarshal errored on unknowns, ingest would fail hard on the first
  new value. Preserve + `known=false` lets us log a warning and
  continue.
- **Subst collapsed to one constant via prefix-parse**. Vendor
  sends `"Substitution 1"`, `"Substitution 2"`, ... — per-team
  numbering we don't care about. Canonical is `"Substitution"`;
  numeric suffix dropped. If we ever start tracking substitutions
  we can un-collapse.
- **Domain imports infra** for the enum types. Debated briefly but
  went pragmatic: `apifootball/enums.go` has no internal deps
  (stdlib only), so it's a safe leaf for domain to import. Cleaner
  than shuffling types into a new shared package.
- **Went Large scope** over Medium (per user judgment call).
  Detail enums have low current ROI (we don't track Yellow Card /
  Subst / Var event details for outbound logic), but VAR tracking
  is on the near-term roadmap and doing it later would be a
  migration commit. One-time investment now.

**Enum coverage counts**:

- 22 tests pass (all previous tests + 9 new enum-parser tests).
- Boundary case tests cover: known-all-casings, unknown preservation,
  empty input, JSON unmarshal path, Subst prefix-parse for indexed
  variants.

**Deferred**:

- No Parse for the vendor's `Long` field on fixture status
  (`"Match Finished"`, `"First Half, Kick Off"`, etc.). Kept as raw
  string for logging/debug only — nothing switches on it.
- Domain event's `RemovalReason` type stays as-is (already a typed
  enum, just not vendor-shaped).

---

## 2026-07-09 — Cross-workflow config centralized in WorkflowsConfig

Prior state had `defaultActivationWindow = 30 * time.Minute` declared as
a const in both `internal/workflow/ingest.go` AND
`internal/workflow/monitor.go`. Same value, two places — one accidental
edit away from silent divergence. User called this out.

Deeper issue: the 30-min activation window is derived from the 15-min
staging poll interval (2× the interval, so a fixture whose kickoff lands
between polls still activates before its kickoff). Making activation
window the config source hid this relationship.

**What shipped**:

- New `internal/config/workflows.go` with `WorkflowsConfig`. Fields:
  - `StagingPollInterval` — SOURCE OF TRUTH for the activation window
    (default 15m). How often MonitorWorkflow's designed 15-min staging
    poll fires. Not yet shipped as behavior; value lives here so
    wire-up doesn't need a config refactor.
  - `ActivationMultiplier` — integer (default 2). Bounds the derived
    activation window.
  - `ActiveFixturePollInterval` — Monitor's schedule interval
    (default 30s). Wired at cmd/worker/main.go into the
    `ScheduleIntervalSpec`.
  - `RetentionDays` — completed-fixture prune threshold (default 14).
    Mirrors Python's `FIXTURE_RETENTION_DAYS`.
- Method `WorkflowsConfig.ActivationWindow() time.Duration` returns
  `ActivationMultiplier × StagingPollInterval` (with a floor of 1× so
  a misconfigured 0 doesn't produce a zero-duration window). No
  separate `ActivationWindow` config field — the invariant can't
  silently drift.

**Design choices logged**:

- **Method + multiplier over method-only**: user picked the multiplier
  approach after I initially recommended method-only. Right call — the
  invariant "activation is always integer × poll" is still enforced;
  the multiplier just lets us tune the multiplier value without a code
  change. Bounded config knob, not a foot-gun.
- **StagingPollInterval as source, not ActivationWindow**: derives from
  the design decision that the staging poll interval is the natural
  cadence and the activation window follows from it. Reversing the
  direction (making ActivationWindow the source) would let someone bump
  it to 45m with a 30s poll — technically valid but semantically wrong.
- **ActiveFixturePollInterval wired into the schedule spec**: `cmd/worker/main.go`
  reads `deps.Cfg.Workflows.ActiveFixturePollInterval` for the Monitor
  schedule's `Intervals` field. Note: changing this env var doesn't
  update an ALREADY-CREATED schedule (Temporal state persists) — you
  have to delete + recreate the schedule to change intervals. Same
  applies if we ever need to change the Ingest schedule's cron.

**Threading**:

- `ingest.Activities` gained `ActivationWindow` + `RetentionDays` fields.
  Populated in `cmd/worker/main.go` from `deps.Cfg.Workflows.ActivationWindow()`
  and `deps.Cfg.Workflows.RetentionDays`.
- `monitor.Activities` gained `ActivationWindow` + `StagingPollInterval`
  fields (StagingPollInterval defined but not yet consumed — future
  staging-poll implementation will read it).
- Workflows read the values via new zero-cost activities
  `GetIngestConfig` / `GetMonitorConfig` (workflows can't touch config
  directly per Temporal determinism).
- Workflow tests updated: `newEnv` / `newMonitorEnv` register default
  `.Maybe()` mocks so tests that don't care about config still work.

**Deferred**:

- 15-min staging poll behavior itself — designed 2026-07-07, config
  now in place, wire-up is next.
- Startup-time assertion that ActivationMultiplier × StagingPollInterval
  produces a sane value (e.g. > 5s and < 4h). Currently only bounded
  by "don't produce zero duration."

---

## 2026-07-09 — Ingest regression fix: dynamic top-flight team lookup + per-day fetch + smart lookahead

**The bug**: dev postgres had zero fixtures across all states after
overnight scheduled Ingest ran. Investigation showed the Ingest activity
was calling `/fixtures?from=X&to=Y` bare — the vendor returns empty
responses for that query shape. Every vendor-doc `from/to` example includes
at least one additional filter (league/team/season); bare from/to is
silently broken. Introduced during Phase O1a, never caught because tests
mock the API and live-verify hit `date=` and `ids=` paths only, not
`from/to`.

**The Python reference impl** (`archive/src/api/api_client.py:fixtures`
+ `archive/src/utils/team_data.py`):

- Fetches per-day via `/fixtures?date=YYYY-MM-DD` (one HTTP call per day).
- Filters returned fixtures by a dynamic team-ID set: fetches top-5
  European league team rosters via `/teams?league=X&season=Y`, caches
  in Mongo for 24h.
- IngestWorkflow does smart lookahead: fetches today + tomorrow always;
  if tomorrow is empty, scans up to 30 days ahead for the next non-empty
  day and fetches that + day-after for timezone coverage.

**What shipped in Go (this commit)**:

- **New pg schema**: `tracked_teams_cache` table (team_id PK, league_id,
  league_name, team_name, season, refreshed_at). Small (~150 rows
  typical), single truncate-and-copy refresh path via transaction.
- **New adapter methods**: `GetCurrentSeason(leagueID)` via
  `/leagues?id=X` reading `seasons[].current`; `ListTeamsForLeague(leagueID, season)`
  via `/teams?league=X&season=Y`. Response types `APILeague` +
  `APITeam`.
- **New domain package**: `internal/domain/team` — `TrackedTeam` value
  type, `Set` for O(1) filter lookup, `Repo` interface with `List`,
  `OldestRefreshedAt`, `Replace`.
- **New pg repo**: `internal/infra/pg/team_repo.go` — `Replace` is
  atomic (TRUNCATE + `pgx.CopyFrom` in a single transaction).
- **Ingest activity changes**:
  - Removed broken `FetchFixturesForWindow(from, to)` bare from/to path.
  - New `FetchFixturesForDay(date)` — single day, filtered by tracked
    team set. Workflow orchestrates the day-by-day loop.
  - New `RefreshTrackedTeamsIfStale` — checks cache age vs
    `TopFlightCacheHours` config; refreshes by looping tracked leagues
    if stale. Fail-open on total failure so ingest keeps working with
    the previous cache.
  - New `GetIngestConfig` — trivial config accessor exposing
    `MaxLookaheadDays` to the workflow (workflows can't touch env
    directly per Temporal determinism rules).
- **IngestWorkflow orchestration**:
  - Step 0: RefreshTrackedTeamsIfStale.
  - Step 1a: Fetch anchor day.
  - Step 1b (if `FetchFuture`): fetch anchor+1. If non-empty, also
    fetch anchor+2 for timezone coverage. If empty, scan anchor+2
    through anchor+MaxLookahead for next non-empty day, then fetch
    that + day-after. Matches Python's `MAX_LOOKAHEAD_DAYS=30`.
  - Dedupe fixtures by ID across days.
- **New workflow input**: `FetchFuture bool` — the daily Temporal
  Schedule sets `true` (full window); manual triggers default `false`
  (anchor-day only, for surgical re-ingests without burning quota).
- **Config**: three new fields on `APIFootballConfig`:
  - `TrackedLeagueIDs []int` (env CSV) — leagues to fetch team rosters
    from. Default `39,140,78,135,61,1` (top-5 + WC).
  - `TopFlightCacheHours int` — cache TTL. Default 24 (Python parity).
  - `FetchWindowFutureDays int` — max lookahead scan. Default 30
    (Python parity).

**WC handling — temporary + intentional.** League ID `1` (World Cup)
is in the tracked-leagues env. `RefreshTrackedTeamsIfStale` calls
`/teams?league=1&season=<current>` and unions those 48 team IDs
(includes qualifiers) into the tracked set. Filter then catches any
fixture between two tracked teams — automatically covers France vs
Morocco, Spain vs Belgium, etc. **The general national-team tracking
problem stays open** — user and Vedanta discussed but deferred a
proper design. For any tournament we care about, add its league ID
to the env. When we design the general solution, this env-based
approach gets replaced.

**Live verification** — manual `trigger_ingest` against v3 API:

- TrackedTeams refreshed: 144 total (WC=48, PL=20, La Liga=20, Serie A=20,
  Bundesliga=18, Ligue 1=18).
- Fetched: 14 fixtures across today + tomorrow + day-after.
- Filtered out: 882 global fixtures where neither team was tracked
  (validates the filter is doing real work, not fail-open).
- Categorized: 12 staging, 1 active, 1 completed. Includes France vs
  Morocco WC 20:00 UTC (staging), Spain vs Belgium WC 7/10 (staging),
  Norway vs England WC 7/11 (staging).
- Monitor picked up the live friendly (Chemnitzer vs Union Berlin,
  1H) and captured a real goal event in the 24th minute — debounced
  through the symmetric counter to stable state within a few 30s
  cycles. First real end-to-end pipeline execution in the Go rebuild.

**Deferred follow-ups** (not blocking):

- Static UEFA fallback for team-refresh total failure (Python has
  `TOP_UEFA_IDS = [15 clubs]`; Go fails open with empty cache instead).
  Different failure mode; not necessarily worse.
- Frontend refresh notification at end of Ingest (Python signals;
  Go waits for NATS wire-up).
- General national-team tracking design (currently piggy-backing on
  tournament league IDs).
- Removed `FetchFixturesForWindow` type left one dead spot in
  workflow tests — cleanup opportunity if we ever pass a broader
  input shape.

---

## 2026-07-09 — apifootball adapter: bugfixes + chunk-parallel refactor

Two-part change on top of the earlier same-day docs-seeding entry.

**Part 1: bugfixes from the doc-seeding follow-ups.**

Both flagged in the earlier entry, both verified live against the
real API after fix:

- Auth header: `x-rapidapi-key` → `x-apisports-key`. Regressed
  somewhere during Phase S7.1; Python's `api_client.py:api_key`
  had always used the doc-correct name. The API tolerated the
  RapidAPI header (why Ingest worked despite the bug), but doc-
  correct + Python-matching is `x-apisports-key`.
- Rate-limit header parsing wired backwards:
  - `x-ratelimit-requests-remaining` (name has "requests") is the
    DAILY quota per doc → wired to `dailyQuotaRemain` gauge.
  - `X-RateLimit-Remaining` (no "requests") is the PER-MINUTE burst
    per doc → wired to `rateLimitRemain` gauge.
  - Old code read a phantom `x-rapidapi-requests-remaining` header
    that api-sports.io never sends.

**Part 2: ListFixturesByIDs refactor — chunk-parallel + partial return.**

Old shape: client hard-rejected >20 IDs. Workflows had their own
`chunkIDs` helper (`internal/workflow/monitor.go`) + fanned out one
Activity per chunk via `workflow.ExecuteActivity`. IngestWorkflow
comment claimed "does NOT chunk" — latent bug: a manual-override
call with >20 IDs would fail hard.

New shape: `Client.ListFixturesByIDs(ids)` accepts any size, splits
internally at `IDsBatchLimit=20` (now exported), fires per-chunk
HTTP calls in parallel via `errgroup`, returns
`(fixtures, failedIDs, err)`. Partial failure is expressed via
non-empty `failedIDs` with `err=nil`; catastrophic failure (all
chunks failed / ctx cancelled) surfaces as an actual error.

**Why partial-return over Temporal-native fan-out (workflow-side
Activity-per-chunk):** discussed at length with user before code
changes. The retry-granularity story that "Activity per chunk gives
per-chunk retry" turned out to matter less than expected for the
Monitor workload — Monitor polls every 30s, so the poll IS the
retry, and per-chunk Temporal fan-out costs 3× Temporal history for
recovery that's already ~30s. Client-side parallel goroutines +
FailedIDs return value + per-workflow retry policy gives the same
parallelism at 1/3 the Temporal overhead.

Per-workflow retry policy:

- **MonitorWorkflow**: no explicit retry. Just logs `MissedIDs`
  count. Next 30s cycle re-requests missed IDs naturally via the
  full active-fixtures pull. Cheaper than Temporal-native retry
  and equivalent in outcome.
- **IngestWorkflow (manual-ID path)**: explicit workflow-level
  loop, 3 attempts, 5s×attempt backoff, TARGETED at only the
  FailedIDs from the previous attempt. Ingest is daily — recovery
  in-cycle beats waiting 24h. Also fixes the latent bug where
  >20 manual IDs would fail hard.

**Live verification** — `scripts/verify_apifootball` hit real
`v3.football.api-sports.io` and confirmed end-to-end:

- Auth via `x-apisports-key`: OK (plan=Pro, 7500/day quota).
- 15-ID single-chunk: 15/15 back, 0 failed, one HTTP call.
- **50-ID multi-chunk: 50/50 back, 0 failed, 0.12s wall-clock** —
  three chunks (20+20+10) fired concurrently. Sequential would have
  been ~600-1500ms; parallel achieved ~5-10× speedup.
- Both rate-limit gauges populated correctly from doc-correct
  headers: `ratelimit_remaining 296` (per-minute), `daily_quota_remaining 7453`.

**Follow-up items (deferred, not blocking):**

- `FixtureListParams` missing a `Live` field — can't currently
  hit `/fixtures?live=all` through the adapter. Not needed by any
  workflow today; add when a caller wants it.
- `ListFixtures` (window/date variant) doesn't yet support the
  same failedIDs semantics; not applicable since it doesn't chunk
  (no `ids=` param).
- Verify script currently uses raw `fmt.Println` for output. If
  we start capturing verify runs into CI or Loki, promote to the
  standard log emitter.

---

## 2026-07-09 — API-Football docs archived + frozen reference seeded

Vendor docs at <https://www.api-football.com/documentation-v3> are
behind a Cloudflare bot challenge — no agent-side WebFetch or curl
UA-spoof gets through. Rediscovering the API's shape from Python +
observation had become expensive (two nights this week alone —
"Red Card" vs "Red card" casing was one of them). Fixed the whole
class of problem in one pass.

**What shipped:**
- PDF export of the docs site (v3.9.3, 131 pages) archived at
  `docs/api-football/vendor/api-football-v3.9.3.pdf` (`git mv` from
  repo root, where it was accidentally committed the night before).
- Full browser-save HTML mirror at
  `docs/api-football/vendor/api-football-v3.9.3.html` — grepable /
  searchable by agents. Asset dir gitignored (5 MB of screenshots).
- Four frozen reference docs under `docs/api-football/` seeded from
  the archive: events-shape.md, fixtures-endpoint.md, status-codes.md,
  rate-limits.md. Each cites the specific PDF page it was seeded
  from.
- `.gitignore` line 109 `vendor/` → `/vendor/` (Go convention is
  root-only; the un-anchored form would also catch the vendor-doc
  archive).

**Key findings that resolve open questions:**

1. **PST → NS on reschedule (RESOLVED — was open in 2026-07-07 entry).**
   Doc explicitly states: *"Postponed to another day, once the new
   date and time is known the status will change to Not Started."*
   Fixture IDs are immutable across this transition. Our current
   worker doesn't watch for date-field changes on active fixtures
   — a deferred behavior noted in the 2026-07-07 "fixture activation
   triggers" entry. Now that we know the transition is a real
   thing, the deferred handler is worth implementing when a real
   PST-reschedule surfaces in prod.

2. **ABD may or may not reschedule.** Doc says *"Can be rescheduled
   or not, it depends on the competition."* We treat ABD as Terminal
   (drops out of Monitor). Reschedules would only be caught by
   daily re-seeding, not in-cycle recovery. Follow-up: should ABD
   get PST-like Live treatment? Not urgent; leave until we see one
   in prod.

3. **429 is NOT documented in the /fixtures response set.** Only
   200 / 204 / 499 / 500 are documented. The doc's Rate Limiting
   Policy says *"Excess traffic may be temporarily or permanently
   blocked without notice"* — vendor-choice error, no promise of
   429. Our adapter's 429-specific handling is defensive against
   observed prod behavior, not doc-specified — kept, but the
   frozen doc now reflects reality.

4. **Rate-limit header names — two axes, easy to confuse.**
   `x-ratelimit-requests-limit` / `-remaining` are **daily**.
   `X-RateLimit-Limit` / `-Remaining` (no "requests" segment,
   mixed case) are **per-minute**. Our older `rate-limits.md`
   stub speculated `x-rapidapi-*` was daily — wrong; that's a
   different vendor's convention.

**Follow-up items surfaced (not blocking):**

- `internal/infra/apifootball/client.go:137` sets
  `x-rapidapi-key` for auth. Direct API-Sports endpoint (which
  we're on) uses `x-apisports-key` per doc page 1. The API may
  tolerate both, but the doc-correct header is the API-Sports
  one. Verify + fix.
- `internal/infra/apifootball/client.go:209` reads
  `x-rapidapi-requests-remaining` — a phantom header not in the
  API-Sports docs. Should read `X-RateLimit-Remaining` for the
  per-minute value.
- Both are silent-degrade paths (missing auth would 401; missing
  header just doesn't observe the metric) — not blocking, but
  worth a cleanup pass.

**Human update flow** for the frozen docs: when the vendor updates
the site, re-save PDF + HTML, drop into `vendor/`, sed the HTML's
asset paths, update the version in filenames, and reconcile the
seeded markdown files. Precedence for source-of-truth remains:
vendor archive > seeded markdown > Python config > adapter
observation.

---

## 2026-07-08 — Test corpus harness Phase 1a shipped + activity clock injection pattern

Ships the minimum viable scenario harness designed in
[`proposals/test-corpus.md`](design/proposals/test-corpus.md). The
"catch prod-class bugs before shipping" testing gap Python has had
forever, and that we've been building against without.

**What shipped in Phase 1a:**
- `test/harness/` — scenario loader (scenario.go), testcontainer pg
  helper (pg.go), httptest mock apifootball (mock_api.go), Tier 1
  assertion engine (assertions.go), scenario runner (runner.go)
- `test/scenarios/basic/ingest_happy_path.yaml` — first scenario
- `test/scenarios_test.go` — the test binary; iterates all YAML under
  `test/scenarios/`, dispatches each to `harness.RunScenario`
- `Makefile` — new `test-corpus` target

**First scenario passing end-to-end.** Total runtime: ~2-3s (mostly
testcontainer pg boot). Actual scenario execution: ~40ms. Full
corpus of 50 scenarios projected <90s per the proposal target.

**Real bug caught during first-run** — the harness immediately
surfaced a determinism issue: `ingest.Activities.now()` reads
wall-clock time, so a scenario with kickoffs relative to
2026-07-07 would non-deterministically activate fixtures whose
kickoff is "in the past" relative to today's real clock.

**Fix pattern established: activity clock injection from scenario.**
The runner sets `acts.Now = func() time.Time { return
scenario.ManualDate.UTC() }` when a scenario declares a `manual_date`.
Activities that need "now" go through this injected clock; scenarios
control what time the activity sees. Every new activity (monitor,
discovery, etc.) MUST expose an injectable clock — same discipline
as `ingest.Activities.Now`. Non-injectable clocks are a determinism
bug caught immediately by the harness on first scenario.

**Why this matters strategically:**
The user's currently-live prod pain ("goal stuck in debounce, video
not found for 5 minutes") is exactly the class of bug the corpus can
catch — interaction of API flakiness with debounce state can be
reproduced in a scenario and asserted against forever. Once we have
Loki logs of a prod bug, the FIRST step becomes "write a regression
scenario that reproduces it" — then we can iterate on the fix with
tight feedback.

**Design decisions logged during implementation:**
- **Truncate between scenarios** (not scenario-prefixed IDs). Chose
  simplicity + guaranteed clean slate.
- **Scenarios at `test/scenarios/`** (repo root). Not `internal/` —
  integration-test-scoped.
- **Every scenario runs on every push via `make test`.** Corpus is
  fast enough not to gate behind nightly.
- **Suites are subdirectories** under `test/scenarios/`. Test name
  includes the suite path so filtering works:
  `go test -run TestScenarios/basic ./test`.
- **Scenario file specifies `workflow: <Name>`** — runner dispatches
  to per-workflow handlers. Currently only "IngestWorkflow"
  supported; adding MonitorWorkflow just extends the switch.

**Deferred to future phases:**
- Tier 2 assertions (workflow spawns, video shares, semantic events)
  — build when scenarios need them
- Tier 3 (log lines, metrics, timing bounds) — same
- Per-cycle API responses (currently one blob per scenario) — extend
  when Monitor scenarios arrive
- Real Temporal server (currently in-memory testsuite) — probably
  never; live smoke via `scripts/trigger_ingest` covers that gap

Next scenarios to write (before Monitor code lands, so the code
comes to fit these):
- `debounce/var_overturn.yaml` — count 1→2→3→trigger→2→1→0→soft-delete
- `debounce/flicker_no_reset.yaml` — the symmetric counter's
  differentiator vs Python
- `basic/ingest_manual_ids.yaml` — manual re-ingest path
- More as needed.

---

## 2026-07-07 — Symmetric-counter debounce (Go rebuild's improvement over Python)

Designed during O2 planning, implemented in fix 3b. Replaces Python's
asymmetric two-array model (`_monitor_workflows` monotonic + reset-on-
presence `_drop_workflows`) with a single counter that oscillates
0..3, incrementing on presence votes and decrementing on absence
votes.

**Model (single counter per event):**
- `debounce_count` INT in `events` table, CHECK `BETWEEN 0 AND 3`
- Seeded at 1 by `Insert(event, workflowID)` — the first-see IS the
  first vote
- Presence vote (from a new workflow_id) increments (LEAST +1, cap 3)
- Absence vote (from a new workflow_id) decrements (GREATEST -1,
  floor 0)
- Vote idempotency enforced by PRIMARY KEY on
  (event_id, workflow_id) in `event_monitor_workflows` and
  `event_drop_workflows` — retrying activities can't double-count
- On first crossing to 3: `downstream_triggered` flag flips
  FALSE→TRUE atomically (one-way). Caller spawns downstream
  workflows exactly once.
- On hitting 0: same transaction atomically soft-deletes with
  `removed=TRUE, removed_reason='var', removed_at=NOW()`. Caller
  runs the destroy pipeline (Temporal cancel + video_shares
  soft-delete) — that's a separate activity, not a repo method.

**Differences from Python** (see also `archive/src/data/events.py`
`add_monitor_workflow`, `clear_drop_workflows`,
`add_drop_workflow_and_check`):

1. **Symmetric vs asymmetric.** Python: monitor votes monotonic,
   drop votes fully reset on any presence. Ours: single counter
   steps up on presence, down on absence, cap and floor both
   enforced.
2. **Flicker penalty.** Python: 3 CONSECUTIVE absences required
   for delete (any presence resets the drop tally). Ours: 3 NET
   absences from wherever counter sits. Flicker accumulates.
3. **Hard vs soft delete.** Python deletes the event row. Ours
   sets `removed=TRUE` — preserves audit trail + prevents
   natural_key collision on reappearance (soft-removed row still
   holds the natural_key; monitor's collision handler recognizes
   the removed state and skips re-voting).
4. **`downstream_triggered` semantics.** Python's `_monitor_complete`
   flag stays TRUE forever. Ours: same one-way flip semantic. No
   change here — this part matches.

**Trade-offs of ours vs Python's:**
- Ours penalizes flicker. Two absences with a presence in between
  chips away at durability. Python: the presence would erase both.
  User's call: they prefer explicit accounting over the reset.
- Ours easier to reason about — one counter, symmetric transitions.
- Ours worse under sustained API flakiness (2 absences early cost
  budget that persists). Fine if API is generally reliable.

**Post-removal behavior — event never returns.** Terminal state.
Even if the API brings the same natural_key back, the collision
handler (to be written in monitor activity code) recognizes the
removed row and skips. If a DIFFERENT event with a similar
signature (same player scoring again, say) happens later, it gets
a new seq via natural_key sequencing — no collision.

**Schema additions to `internal/infra/pg/schema.sql`:**
```sql
events.debounce_count INT NOT NULL DEFAULT 1
  CHECK (debounce_count BETWEEN 0 AND 3)
events.downstream_triggered BOOLEAN NOT NULL DEFAULT FALSE
```

**Interface changes** (`internal/domain/event/repo.go`):
- `Insert(ctx, e)` → `Insert(ctx, e, workflowID)` — atomic
  event+vote seed
- ADDED `RegisterEventPresence(ctx, eventID, workflowID) →
  (newCount, justTriggeredDownstream, err)`
- ADDED `RegisterEventAbsence(ctx, eventID, workflowID) →
  (newCount, hitZero, err)` — soft-deletes atomically at zero
- REMOVED (were unimplemented): `RegisterMonitorWorkflow`,
  `RegisterDropWorkflow` — subsumed by presence/absence
- KEPT UNCHANGED: `RegisterVideoValidationWorkflow` — separate
  concern (tracks download attempts, not stability)

**Test coverage** (all pass against testcontainer Postgres):
- Presence climb 1→2→3, only third call justTriggered
- Presence idempotency (same workflow_id retries)
- Presence no-retrigger (cap at 3 post-flip)
- Absence hits zero → soft-delete atomic
- Absence 3→2→1→0 sequence, only last hitZero
- Flicker (present-absent-present) does not hard-reset
- Absence idempotency
- Post-removal absence is no-op (no double soft-delete)

Total: 8 new debounce tests + updated 6 existing CRUD tests for the
Insert signature change.

Devs need to wipe pg volume for schema change:
`docker volume rm found-footy-dev_postgres-data && docker compose
-f docker-compose.dev.yml up -d postgres`. Done in dev; prod
unaffected (still runs Python).

---

## 2026-07-07 — APIStatus bucketing preserves Python's SUSP/INT/PST=active

Preserves Python's status classification (`archive/src/utils/fixture_status.py`)
for the Go rebuild. Load-bearing detail I initially got wrong:

**Python's active bucket**: `1H, HT, 2H, ET, BT, P, LIVE, SUSP, INT,
PST` — the obvious playing codes PLUS three "not-currently-playing
but might resume any minute" codes.

**My initial Go implementation (`internal/domain/fixture/fixture.go`
`APIStatus.Live()`)** was missing `SUSP`, `INT`, `PST`. That would
have caused MonitorWorkflow to skip these fixtures when they should
be polled — real bug, caught during O2 planning.

**Fix**: added `SUSP, INT, PST` to `APIStatus.Live()`. Matches Python
exactly. Test updated to cover the new codes explicitly.

**Why preserve Python's design** (not overhaul):
- Cost isn't wasteful: our API calls are BATCHED via `?ids=...`.
  Adding a PST fixture to the batch costs 0 additional API calls.
  My earlier "PST fixtures burn API budget" concern was wrong; the
  batching model already makes this free.
- Short delays (15-30 min) are common in real matches. Polling PST
  fixtures every 30s means we catch the resume within one cycle.
- Only truly-lost postponed fixtures (never resume) cost anything,
  and those get handled by daily ingest / a future cleanup job.

**Consequences downstream**:
- MonitorWorkflow's active-fixture-list query stays `state = active`
  regardless of underlying api_status. No distinct `postponed` state
  added.
- A fresh fixture that ingest sees with `status=PST` gets emergency-
  activated (same as if `status=1H`). Same-day resume within 30 min
  of pre-activation window fires normally.
- The "stuck PST forever" edge case is a real but small residue —
  handled separately by a future cleanup (Python does this via a
  "next day cleanup" I haven't yet grepped).

**Doc updates in same commit** (per working rule):
- `internal/domain/fixture/fixture.go` — `APIStatus.Live()` +
  docstring explaining the classification + Python source cite
- `internal/domain/fixture/fixture_test.go` — test now covers all
  10 Live codes explicitly
- `docs/orchestration.md` — IngestWorkflow initial-state
  paragraph updated to reflect the full Live() code set

**Not decided in this entry** (queued for later O2 questions):
- Whether to add adaptive polling frequency at all
- The "next day cleanup" for stuck PST — need to read Python's impl
  first

---

## 2026-07-07 — O1e complete — schedule registered + all §5 W1 divergences realigned

Closes the O1e sequence started after the retro. All six IngestWorkflow
divergences from plan §5 W1 (see
[original entry](#2026-07-07--ingestworkflow-divergences-from-plan-5-w1))
are now either fixed or explicitly kept-with-rationale.

**O1e/b — daily Temporal Schedule wired.** `cmd/worker/main.go`
`ensureIngestSchedule` runs on worker startup and registers the
schedule if it doesn't exist. Schedule ID
`ingest-scheduled-daily`; cron `5 0 * * *` (00:05 UTC); overlap
policy SKIP; args `IngestWorkflowInput{RetentionDays: 14}` (plan
§5 W1 default).

Idempotent: `ErrScheduleAlreadyRunning` on subsequent restarts is
caught + logged as an "already exists" outcome (new vocabulary
action `temporal_schedule_already_exists`) rather than an error.
Manual updates via `temporal schedule update` are safe.

Verified live in dev:
  ✓ Schedule created on first startup after deletion
    (log: temporal_schedule_created)
  ✓ Schedule "already registered" on restart
    (log: temporal_schedule_already_exists)
  ✓ `temporal schedule list` shows next run 8h from now with the
    expected cron

**Adapter changes:**
- `internal/infra/temporal/client.go`:
  `Client.ScheduleClient() client.ScheduleClient` — passthrough
  accessor to the SDK's ScheduleClient. Not per-op instrumented
  (schedule ops are rare).
- `internal/observability/vocabulary/actions_infra_temporal.go`:
  three new actions — `temporal_schedule_created`,
  `temporal_schedule_already_exists`, `temporal_schedule_failed`.

**Final status of the original 6 divergences from
[2026-07-07 IngestWorkflow entry](#2026-07-07--ingestworkflow-divergences-from-plan-5-w1):**

| # | Divergence | Resolution |
|---|---|---|
| 1 | Input shape | Realigned in O1e/a (fixed) |
| 2 | Output shape (Errors []string) | Realigned in pre-O1e cleanup batch (fixed) |
| 3 | ListUniqueTeamsFromFixtures inlined | Kept — defensible improvement |
| 4 | RAG deferral (PreCacheAliasesBatch → placeholder) | Kept — user-approved deferral |
| 5 | ManualFixtureIDs missing | Fixed as part of O1e/a |
| 6 | ActivationWindow added | Kept — user-approved addition |

**Doc updates in same commit** per working rule:
- `docs/deployment.md` — replaces "Workflow scheduling — NOT
  WIRED" section with the wired-and-verified pattern
- `docs/temporal.md` — replaces "Schedule registration —
  NOT YET WIRED" with the actual pattern + load-bearing invariants
  (idempotency, no-overwrite, overlap SKIP)

**Files:**
- `cmd/worker/main.go` — `ensureIngestSchedule` + imports
- `internal/infra/temporal/client.go` — ScheduleClient accessor
- `internal/observability/vocabulary/actions_infra_temporal.go` — 3
  new actions
- `docs/deployment.md` — schedule section
- `docs/temporal.md` — schedule section

Phase O1 is now genuinely complete — including doc discipline,
input/output realignment, and schedule wiring. Ready for O2
(MonitorWorkflow) after any planning discussion.

---

## 2026-07-07 — O1e/a — IngestWorkflow input reshape complete

Realigns three of the six IngestWorkflow divergences from plan §5 W1
that the retro logged (see [earlier entry](#2026-07-07--ingestworkflow-divergences-from-plan-5-w1)).

**Changes:**

**1. Input shape → plan §5 W1 + ActivationWindow.**
```go
type IngestWorkflowInput struct {
    ManualDate       *time.Time
    ManualFixtureIDs []int64
    ActivationWindow time.Duration
    RetentionDays    int
}
```
Was:
```go
type IngestWorkflowInput struct {
    FetchWindowFrom    time.Time
    FetchWindowTo      time.Time
    ActivationWindow   time.Duration
    RetentionThreshold time.Time
}
```
Behavior: workflow computes fetch window from anchor (ManualDate or
workflow.Now) using plan's `[anchor-1d, anchor+3d]` bracket. Retention
cutoff is `anchor - RetentionDays*24h`. Everything derives from the
anchor so manual-date re-ingest is consistent.

**2. `ManualFixtureIDs` path added.** When populated, workflow
dispatches to a new `FetchFixturesByIDs` activity (thin wrapper over
`apifootball.ListFixturesByIDs`, existing adapter method from O1a).
`FetchFixturesForWindow` is bypassed entirely. Cap: 20 IDs per call
(api-sports.io limit; workflow does NOT chunk — callers must batch).

**3. `RetentionDays int` replaces `RetentionThreshold time.Time`.**
Zero still means "skip prune." The schedule spec sends 14 explicitly
(not injected as default at workflow level — callers own the value).

**Test updates:**
- Two new activity tests: `TestFetchFixturesByIDs_HappyPath`,
  `TestFetchFixturesByIDs_PropagatesError`
- Two new workflow tests: `TestIngestWorkflow_ManualFixtureIDs_UsesByIDsPath`,
  `TestIngestWorkflow_EmptyInput_UsesDefaults`
- Updated `stdInput` helper + `TestIngestWorkflow_ZeroRetention_SkipsPrune`
  for the new shape
- Total ingest+workflow tests: 23 (was 19), all passing

**`scripts/trigger_ingest/main.go` simplified:** now passes empty
input; workflow self-configures with `workflow.Now` as anchor + skip
prune (dev safety default).

**Doc updates in same commit** per working rule:
- `docs/orchestration.md` — new input shape + branching
  activity sequence + anchor propagation notes.

**Two divergences from the original 6 still open:** input reshape (this
entry — DONE) and output reshape / Errors []string (done in the
[2026-07-07 pre-O1e cleanup batch](#2026-07-07--pre-o1e-cleanup--lastpolledat-fix--errors-string)).
Remaining unfinished O1e item: schedule registration (O1e/b, queued).

---

## 2026-07-07 — Pre-O1e cleanup — LastPolledAt fix + Errors []string

Small pre-O1e batch, three changes to `internal/activity/ingest/`:

**1. `LastPolledAt` now set on all ingest paths.** Was a real bug:
ingest hit api-sports.io (that IS a poll) but the fixture's
`LastPolledAt` field stayed nil on fresh rows and stale on existing
rows. Consequences would have been: MonitorWorkflow's future poll-
bucket logic re-polls every fixture on its first cycle after ingest,
wasting the amortization the whole scheme exists to provide. Fixed
on both branches of `reconcileFixture` (existing: `existing.LastPolledAt
= &now`; fresh: `f.LastPolledAt = &now` before state transitions,
which don't touch that field). Two new regression tests
(`TestCategorize_SetsLastPolledAt_OnFresh`,
`TestCategorize_UpdatesLastPolledAt_OnExisting`).

**2. Removed redundant `f.CreatedAt = now; f.UpdatedAt = now`** from
`reconcileFixture` fresh branch. `fixture.New` sets both internally
via `time.Now().UTC()`; state transitions (Activate/Complete)
overwrite UpdatedAt anyway. Manual re-sets were dead code. Off-by-ns
CreatedAt drift vs the injected test `now` is harmless (no test
asserts on it — the field is a "when was this row born" audit
signal).

**3. `Errors int` → `Errors []string` with context.** Was one of
the 6 IngestWorkflow divergences from plan §5 W1 logged in the retro
as "silent, realign in O1e." Doing it now as part of the small
batch. Per-fixture / per-team failures inside activity loops now
land as strings like `"reconcile fixture=1515514: pool exhausted"`,
aggregated at the workflow level into `IngestWorkflowOutput.Errors
[]string`. Operators see WHAT failed and WHY in Temporal UI without
joining logs. Deleted the workflow's `CategorizeErrors int` and
`AliasErrors int` fields (subsumed by the aggregated Errors slice).
One new test (`TestCategorize_ErrorsCarryFixtureContext`).

**Doesn't touch input shape.** Full O1e (input reshape to
`ManualDate + ManualFixtureIDs + RetentionDays`, schedule
registration) is still queued. This batch fixes the specific gaps
the user surfaced without expanding scope.

**Doc updates** (per the same-commit rule):
- `docs/orchestration.md` — new I/O shape + LastPolledAt
  notes in the reconcile-logic section.

**Files:**
- `internal/activity/ingest/activities.go` — the three changes
- `internal/activity/ingest/activities_test.go` — 3 new tests
- `internal/workflow/ingest.go` — `IngestWorkflowOutput.Errors`
  aggregation
- `internal/workflow/ingest_test.go` — no changes needed (mocks
  don't reference removed fields)

Test count: 19 in ingest + workflow (was 16), all passing.

---

## 2026-07-07 — Ripped `internal/errors/` stub

Plan §2 tree listed `internal/errors/ # typed error taxonomy`. Shipped
state until today: a 5-line `doc.go` with no types, no functions, no
imports from anywhere in the codebase.

**Decision:** delete the file + directory. Reconsidered under the
`docs/design/rebuild-plan.md` = intent / `docs/rebuild/*.md` = ledger
framing: an empty stub sends a **false signal** that "typed error
taxonomy lives here" when it doesn't. Every adapter defines its own
error types locally (LLM has typed errors, apifootball has HTTP
status classification, etc.). Cross-cutting sentinels haven't been
needed yet because no workflow branches on error class — IngestWorkflow
uses uniform exponential backoff regardless of failure mode.

**When to rebuild:** when the first workflow (probably MonitorWorkflow
in O2, more likely DiscoveryWorkflow in O3) needs `errors.Is(err,
ffserrors.ErrRateLimited)` to distinguish "back off harder" from
"give up." At that point create `internal/errors/errors.go` fresh
with only the sentinels we actually need. Each adapter's own errors.go
wraps with `fmt.Errorf("apifootball: quota exhausted: %w",
ffserrors.ErrRateLimited)`.

**Trigger criteria:** a workflow's retry policy needs to branch on
error class, OR two different adapters emit conceptually-identical
errors (e.g. both LLM and apifootball emit rate-limit) that a
workflow needs to treat uniformly.

**Not building this now on the "plan said so" reason.** Follows
the CLAUDE.md rule against half-finished implementations. Follows the
"don't add abstractions beyond what the task requires" rule.

Ripped in the same commit as this entry. Two-second `git rm`; two-second
recreation later when actually needed.

---

## 2026-07-07 — Working rule: living docs update in the same commit as code

Retrospective response to a stretch of ~15 commits (S1–O1d) where code
shipped without corresponding updates to `docs/rebuild/*.md`.

**Rule.** Every implementation commit that adds/changes a package,
adapter shape, workflow, or activity MUST update the relevant living
doc (usually `docs/architecture.md` or
`docs/orchestration.md`) in the SAME commit. If it diverges
from `docs/design/rebuild-plan.md`, `docs/decisions.md` gets an entry.

**Why.** The plan is intent — 12k lines, written before implementation,
not per-commit. The per-topic docs in `docs/rebuild/` were meant to be
the ledger of what actually shipped and are currently empty stubs.
Silent code-only commits meant the plan drifted from reality without
either doc surfacing that fact. This retro compensates. Going forward,
the rule prevents recurrence.

**Enforcement.** An implementation commit without a corresponding doc
update is treated as incomplete — same status as one missing tests.

---

## 2026-07-07 — Doc retro closure

Sixth and final commit in the retrospective doc-fill sweep that began
after realizing S1–O1d shipped without living-doc updates.

**What got done (6 commits over ~2 hours):**

1. `docs/architecture.md` — filled with as-shipped tree,
   per-package status, adapter template.
2. `docs/orchestration.md` — filled with workflow inventory
   + IngestWorkflow ledger.
3. `docs/observability.md` + `logging.md` — filled with
   four-pillars status + vocabulary + Emit reference. (Note: first
   attempt at this had a broken commit — Write failed silently,
   commit message lied; caught + fixed in follow-up.)
4. `docs/temporal.md` — filled with Client/Worker shape +
   registration flow.
5. `docs/testing.md` + touched-up `deployment.md` — filled
   test tier ledger.
6. `CLAUDE.md` phase table + `docs/design/README.md` status +
   this closure entry.

**Divergences logged in this sweep (5 groups):**

- Working rule (this rule)
- Architecture divergences from plan §2 (7 items: migrations empty,
  bootstrap added, 4 internal/ packages stubbed, 4 domain packages
  stubbed)
- IngestWorkflow divergences from plan §5 W1 (6 items — 3 sanctioned,
  3 silent, 3 slated for realignment in O1e)
- Log-catalog generator §11.3 deferred (rationale: grep is still
  ergonomic at ~50 actions)
- Temporal adapter divergences from plan §9 (5 items — 4 kept as
  improvements, 1 sensible addition)

**Stubs NOT filled** (deferred with rationale, not omissions):
- `docs/design/api-contract.md` — Phase A hasn't shipped; nothing
  to ledger yet.
- `docs/operations.md` — Phase M/C bring-up procedures
  aren't real yet.

**What resumes:** Phase O2 planning (MonitorWorkflow) — but only
after O1e realignment commits fix the input/output-shape divergences
from plan §5 W1 that this retro surfaced. Order:

1. **O1e/a**: Realign IngestWorkflow input to plan shape
   (ManualDate + ManualFixtureIDs + RetentionDays) — with tests +
   scripts/trigger_ingest update + doc update.
2. **O1e/b**: Realign IngestWorkflow output to include Errors []string.
3. **O1e/c**: Register daily Temporal Schedule for IngestWorkflow.
4. **Only then**: O2 begins.

Retro complete. Living-doc-with-code discipline stands as the working
rule going forward.

---

## 2026-07-07 — Temporal adapter divergences from plan §9

Three divergences between `internal/infra/temporal/` (shipped in S5)
and plan §9's temporal spec. Retrospectively logged as part of the
doc retro.

**1. `NewClient` takes `*Instruments`, not `*slog.Logger`.** Plan §9
had `func NewClient(ctx, cfg, logger *slog.Logger) (client.Client, error)`.
Shipped: `func NewClient(ctx, cfg, ins *Instruments) (*Client, error)`.
**Silent.** Instruments carry logger + metrics + (eventually) tracing
together; the plan's logger-only param would be an outlier vs every
other adapter (S2+). **Keep — consistent with adapter template.**

**2. `Client` wraps SDK type; doesn't return raw `client.Client`.**
Plan §9 wanted `(client.Client, error)` return. Shipped: `(*Client,
error)` with our type. **Silent.** Rationale: (a) `WorkerShutdownTimeout()`
accessor for graceful shutdown ordering, (b) our `Close()` hook can
emit metrics, (c) tracing hookup later without changing callers.
**Keep.**

**3. `worker.Options` param on `NewWorker`; no hardcoded defaults.**
Plan §9 wanted `NewWorker(c, cfg, logger)` returning a worker with
"sensible defaults" (MaxConcurrentActivityExecutions:30,
MaxConcurrentWorkflowTasks:10) baked in. Shipped:
`NewWorker(c, ins, options worker.Options)` with caller-supplied
options. **Silent.** Rationale: cmd binaries know what they're
running; adapter shouldn't decide concurrency. **Keep.**

**4. `DefaultRetryPolicy()` helper not shipped.** Plan §9 called for
`func DefaultRetryPolicy() *temporal.RetryPolicy`. Not implemented.
Each workflow defines its own `workflow.ActivityOptions` inline
(e.g. `internal/workflow/ingest.go`). Rationale: retry policy visible
at call site is easier to audit than a shared default that hides its
config. **Keep the divergence; delete the plan's helper on next plan
revision.**

**5. `SignalWorkflow` method added on Client.** Not in plan §9.
Sensible addition; kept for future AssetPersistenceWorkflow signal
paths.

---

## 2026-07-07 — Log-catalog generator (§11.3) not shipped

Plan §11.3 specifies a generated `docs/generated/log-catalog.md`
regenerated on every build via `go generate`, listing every
(vocabulary.Module, vocabulary.Action) pair with expected field set
and log-level guidance.

**Status:** not implemented in S1 or since.

**Rationale for the deferral:** with ~50 declared actions across
11 adapter modules + 5 workflow modules today, `grep -r "vocabulary\.Action"`
is still ergonomic. Generator adds a build-time dependency (needs a
tool + a generate marker + a CI step to keep the checked-in catalog
current) that's cheap to build later when the surface grows and hard
to justify now.

**Trigger to build:** when the vocabulary passes ~200 declared
actions OR when someone asks "what actions does X module emit?" and
grep isn't fast enough OR when we introduce webhook consumers that
need a discoverable catalog to subscribe to.

**Not a design change.** The plan's shape (per-(module, action)
markdown table) still stands; only the timing shifts.

---

## 2026-07-07 — IngestWorkflow divergences from plan §5 W1

Six divergences between the shipped IngestWorkflow (O1a-O1d) and the
plan's §5 W1 spec. Retrospectively logged as part of the
2026-07-07 doc retro. Three are user-approved conversationally,
three were silent implementation choices — decisions on whether to
keep or realign made below.

**1. Input shape.** Plan wanted:
```go
type IngestWorkflowInput struct {
    ManualDate       *time.Time   // nil for scheduled; set for manual re-run
    ManualFixtureIDs []int64      // nil for full-window; set for ad-hoc re-ingest
    RetentionDays    int          // default 14
}
```
Shipped:
```go
type IngestWorkflowInput struct {
    FetchWindowFrom    time.Time
    FetchWindowTo      time.Time
    ActivationWindow   time.Duration
    RetentionThreshold time.Time
}
```
**Silent.** My choice, not discussed. Consequences: no ad-hoc
per-ID re-ingest (already bit me — `scripts/trigger_ingest`
hardcodes a window because `ManualFixtureIDs` doesn't exist).
Explicit window params are more general but every caller (schedule +
manual trigger) has to compute them; plan's `ManualDate` +
schedule-default was simpler for the common case.
**Decision:** realign to plan shape in **O1e follow-up commit**.
Add back `ManualFixtureIDs` for ad-hoc re-ingest. Keep
`ActivationWindow` as an addition (see #6). Convert
`RetentionThreshold time.Time` back to `RetentionDays int`
(schedule computes cutoff from days).

**2. Output shape.** Plan wanted:
```go
type IngestWorkflowOutput struct {
    FixturesUpserted int
    AliasesCached    int
    FixturesPruned   int
    Errors           []string  // non-fatal errors surfaced for observability
}
```
Shipped: 9 int counters (staging, active, completed, existing,
inserted, aliasErrors, categorizeErrors, prunedFixtures, fetched).
No `Errors []string`.
**Silent.** More granular but loses the observability signal for
non-fatal per-fixture errors that don't fail the workflow.
**Decision:** realign in O1e. Keep the granular counts as fields
alongside `Errors []string`. Both are useful.

**3. `ListUniqueTeamsFromFixtures` inlined into
`CategorizeAndUpsertFixtures`.** Plan had it as a separate activity
step. Shipped: collected via a `map[int]TeamRef` inside the
categorize loop, returned as part of `CategorizeOutput.TeamRefs`.
**Silent.** Rationale for the choice: dedup-by-team-id happens in
memory; extracting it to a separate activity means re-marshaling
the full `[]APIFixture` across the Temporal activity boundary for
what's effectively a pure map-reduce.
**Decision:** keep. Defensible improvement over the plan.
Documented in `docs/orchestration.md`.

**4. `PreCacheAliasesBatch` → `EnsureAliasPlaceholders`.** Plan
had `PreCacheAliasesBatch` doing full RAG resolution (Wikidata
lookup + LLM twitter-alias inference) inline during ingest.
Shipped: `EnsureAliasPlaceholders` inserts blank-resolution
placeholder rows; a separate resolution job (design TBD) fills
them later.
**User-approved conversationally.** Rationale: (a) RAG on hot
ingest couples the daily job to joi + Wikidata availability; (b)
the RAG design itself needs revisiting (team-alias + player-name
search-string building could be improved over Python's approach);
(c) user explicitly said "don't just port Python's logic here."
**Decision:** keep. Retroactively documented.

**5. No `ManualFixtureIDs` support.** Direct consequence of #1.
Plan had it as first-class input for ad-hoc re-ingest of specific
fixtures.
**Decision:** realign as part of #1 fix.

**6. `ActivationWindow` parameter added — not in plan.**
Introduced to fix the Python-era "manual ingest at 14:55 for 15:00
kickoff sits in staging until next monitor cycle" bug (see
[2026-07-07 Fixture activation triggers](#2026-07-07--fixture-activation-triggers--staging-poll-design)).
**User-approved conversationally.** Designed together during
Phase D.
**Decision:** keep. Retroactively documented.

---

## 2026-07-07 — Rebuild architecture divergences from plan §2

Documented here as part of the retrospective (see rule above). Most of
these emerged silently during S1–S7 and D and only got captured after
the fact.

**Divergences from `rebuild-plan.md` §2 tree:**

1. **`migrations/` is empty; schema lives in
   `internal/infra/pg/schema.sql`.** Plan wanted golang-migrate style
   SQL files at repo root. Instead, we ship a single `schema.sql` that
   docker-entrypoint-initdb.d mounts into dev postgres (fresh volume
   only) and testcontainers loads via `WithInitScripts`. Rationale:
   pg pool + schema landed in S2; no migrations tooling was needed
   because dev + test rebuild the DB from scratch. Prod migration
   tooling is a Phase M concern (not yet designed).
   **Decision:** keep `internal/infra/pg/schema.sql` as the
   authoritative source through Phase M. Introduce golang-migrate (or
   equivalent) alongside the Phase M cutover plan.

2. **`internal/bootstrap/` package added — not in the plan tree.**
   Contains `bootstrap.Run(...)` (shared binary startup: config load,
   signal wiring, metrics server, LIFO Closer registry) + `Deps`
   struct passed to each cmd's main. Introduced in S1 to eliminate
   ~50 lines of boilerplate per binary and centralize graceful-shutdown
   ordering. **Decision:** keep — the LIFO Closer registry pattern is
   load-bearing for temporal worker draining (worker Stop must run
   before pg pool Close).

3. **`internal/infra/event/` composer stubbed** (only `doc.go`).
   Plan wants the semantic-event dual-write composer (pg + nats) here.
   Deferred to Phase O2 when MonitorWorkflow starts emitting
   `event.detected` / `event.stable` / `event.removed`.

4. **`internal/infra/ffmpeg/` stubbed** (only `doc.go`). Deferred to
   Phase A (video pipeline).

5. **`internal/usecases/` stubbed** (only `doc.go`). Deferred to when
   the first cross-domain operation surfaces (probably VAR removal
   during O2/O3).

6. **`internal/testutil/` empty.** Build factories/fakes when second
   activity or workflow package needs sharing. Currently the ingest
   activity's fakes are inlined in its test file — cost of premature
   extraction > cost of duplication at N=1.

7. **Domain packages `discovery`, `vision`, `session`, `textanalysis`
   stubbed** (only `doc.go` each). Explicit conversation-approved
   deferral: build each when the corresponding workflow needs it.
   Rationale — building all 8 domain packages up front risks
   speculative modeling; building on demand keeps the shapes tight to
   real callers.

**No divergence** from plan §2 tree for: `cmd/`, `internal/domain/`
(the shipped 4), `internal/workflow/`, `internal/activity/`,
`internal/api/`, `internal/config/`, `internal/observability/`,
`internal/scaler/`, adapter tree (except the two composer stubs
above), `caddy/`, Dockerfiles, Makefile.

(Note: `internal/errors/` was on this list at retro time but has
since been ripped — see [2026-07-07 later entry](#2026-07-07--ripped-internalerrors-stub).)

---

## 2026-07-07 — Fixture activation triggers + staging-poll design

**Fixture activation** (staging → active) fires from three triggers,
all routed through the same `Fixture.Activate(at)` primitive:

1. **Ingest-time pre-activation.** At upsert time, if
   `f.ShouldActivateNow(now, 30min)` is true, the ingest activity
   calls Activate BEFORE the first `repo.Upsert`. The fixture never
   lands in staging in the DB. Fixes the 2026-05 Python-era bug
   where manual ingest at 14:55 for a 15:00 kickoff sat in staging
   until the next 15-min monitor cycle.
2. **Monitor pre-activation.** `MonitorWorkflow`'s
   `PreActivateUpcoming(30min)` activity scans staging fixtures every
   30 seconds and promotes any that ShouldActivateNow returns true
   for. Uses the same helper as (1) so the rule stays one place.
3. **Emergency activation.** If the monitor's API response for a
   staging fixture shows `APIStatus.Live()`, promote immediately
   (already-live match wasn't caught by pre-activation because
   ingest had the wrong kickoff or the API published a corrected
   one). Same `Activate(now)` transition.

**Staging-poll design.** The plan §5.2 as-drafted skipped the
Python-era 15-minute staging API poll, meaning postponements +
kickoff changes on staging fixtures would go undetected until the
fixture activated (which may never happen if the new kickoff is far
out). This was already noted in [`docs/todo.md`](../archive/docs/todo.md) as a
deferred Go-rebuild item after the 2026-07-05 Mexico vs England
postponement surfaced the gap.

Decision for Phase O's MonitorWorkflow: **mirror Python's approach.**
Every 30-second cycle:

- Always: `PreActivateUpcoming(30min)` (DB-only check, no API call)
- Always: fetch API status for active fixtures
- **On 15-min aligned boundaries** (`:00 / :15 / :30 / :45`): also
  fetch API status for staging fixtures whose `LastPolledAt` doesn't
  match the current 15-min interval. Detects postponements,
  kickoff changes, status-flipped-to-live. Per-fixture skip keeps
  the total API burn near-zero when nothing's changing.

The alignment logic is a monitor-workflow concern (schedule decision,
not domain), so it doesn't touch the fixture domain package. What
domain does provide: `ShouldActivateNow`, `APIStatus.Live()`,
`Activate`. Those three primitives compose to cover all three
triggers.

## 2026-07-07 — Workflow renames for Phase O

The Go rebuild renames three of the six Python-era Temporal workflows.
Nothing runs under the Go names yet — workflow IDs live in Temporal
history permanently, so renaming after Phase O ships would leave a
mixed vocabulary in perpetuity. Renaming now = free.

| Python name         | Go rebuild name              | Reason |
|---------------------|------------------------------|--------|
| `IngestWorkflow`    | `IngestWorkflow`             | unchanged — does exactly what it says |
| `MonitorWorkflow`   | `MonitorWorkflow`            | unchanged — poll active fixtures every 30s |
| `TwitterWorkflow`   | **`DiscoveryWorkflow`**      | "Twitter" describes the source; the workflow's job is discovering candidate videos. Rename lets a future YouTube/TikTok/Reddit source fold in without a rename cascade. |
| `DownloadWorkflow`  | **`VideoValidationWorkflow`** | downloads + AI-validates + hashes. "Download" undersells what the workflow does. |
| `UploadWorkflow`    | **`AssetPersistenceWorkflow`** | dedups first, THEN maybe uploads. The dedup is the load-bearing part; "Upload" hides it. |
| `RAGWorkflow`       | **(folded into `IngestWorkflow` as a sub-activity)** | "RAG" is implementation vocabulary. Alias resolution is one step of ingest, not a separate top-level workflow. |

Naming for Temporal task types + workflow IDs follows the new names.

## 2026-07-02 — NATS is metadata-plane only; video bytes go over HTTP

**Decision:** NATS/JetStream carries semantic events, SSE fan-out, and
webhook delivery — all small structured messages. Video bytes are NEVER
routed through NATS. Browsers fetch videos via HTTP/HTTPS directly from
Garage (S3-compatible) through the workspace Caddy, using presigned URLs
returned by the `/api/v1/videos/{share_id}` 302 redirect.

**Why NATS is wrong for video byte transport:**

- **Broker memory + storage blowup**: even at 20 MB per clip and hundreds
  of clips per match day, pushing videos through NATS turns the
  workspace-shared broker into a media store. Wrong tool for the shape.
- **No range-request support**: browsers need `Range: bytes=x-y` to seek
  within a video. HTTP/S3 does this natively; NATS doesn't.
- **Poor latency**: NATS adds a hop the CDN model avoids. Playback
  needs the shortest possible path from bytes-on-disk to browser
  decoder.
- **Wrong caching model**: Caddy + Garage can add edge caching, ETags,
  and Cache-Control; NATS as a message broker doesn't have these
  primitives.

The common misconception is that "large streaming services use Kafka
for video." They don't. Netflix / YouTube / Twitch use Kafka (or
equivalent) for the **metadata plane** — view counts, recommendation
events, ad tracking, telemetry. Bytes always go over HTTP-based
segment protocols (HLS, MPEG-DASH) backed by S3-alike storage + CDN.
Kafka is not in the byte path.

**Why the play-latency problem ("video sometimes needs close+reload
before playing") has a different root cause:**

Almost certainly one or more of:

1. **Video not encoded with `-movflags +faststart`**: MP4's index (moov
   atom) defaults to the END of the file. Browsers can't start playing
   until they've downloaded the whole file. `-movflags +faststart`
   moves the moov atom to the front and the browser starts playing
   after ~500 KB. Biggest single win.
2. **Slow presigned URL generation**: Python's boto3 signing is not
   fast; Go's `aws-sdk-go-v2` signing is significantly faster. Free
   win from the rebuild.
3. **First-byte latency from cold MinIO**: Garage has better cache
   locality by default.
4. **Redirect chain adds RTT**: `Cache-Control` on the 302 lets the
   browser cache the target URL and skip the API round-trip on repeat.

**What ships in the new stack to fix play latency:**

- **`-movflags +faststart` on every downloaded video.** The
  `DownloadVideo` activity (§7) invokes ffmpeg with this flag as part
  of its download/normalize pass. No exceptions.
- **Cache-Control on the 302 redirect** (`Cache-Control: public,
  max-age=300`) so the browser caches the S3 URL for 5 minutes and
  doesn't re-hit `/api/v1/videos/{share_id}` for repeated plays of
  the same clip.
- **Browser preload via SSE `event.video_ready`.** The
  vedanta-systems React component subscribes to the found-footy
  SSE stream and, on `event.video_ready`, sets the `<video src=...>`
  attribute IMMEDIATELY on a hidden video element (browser starts
  buffering). By the time the user clicks, the first frame is ready.
  Zero new backend infrastructure — reuses the NATS event we already
  publish.
- **Optional (Phase C follow-up)**: server-side pre-warm. When
  `event.video_ready` fires, `api` issues a small server-side GET
  against the presigned URL to warm Garage's cache before the user's
  browser asks. Deferred until we have real telemetry showing the
  browser preload isn't enough.

**Consequences:**

- Rebuild plan §7 (video pipeline) documents `-movflags +faststart` as
  a hard requirement of the `DownloadVideo` activity.
- Rebuild plan §8 SSE event catalog notes that `event.video_ready` is
  the load-bearing signal for frontend preload.
- vedanta-systems frontend gets a small change during Phase C cutover:
  the video-tile component subscribes to `event.video_ready` and
  preloads the `<video src>` when it fires.
- The play-latency bug is treated as a **known issue in Python prod
  that the Go rebuild resolves structurally**, not something to
  monkey-patch in Python. If it becomes an urgent user complaint
  before the rebuild lands, we can backport `-movflags +faststart`
  to Python `download_single_video` as a small standalone fix.

## 2026-07-01 — Workspace NATS as event bus (replaces Postgres LISTEN/NOTIFY)

**Decision:** found-footy's async event stream (SSE fan-out, webhook delivery,
cross-project events consumed by vedanta-systems) flows through workspace
NATS at [`~/workspace/nats/`](../internal/infra/nats/), NOT through Postgres LISTEN/NOTIFY
as originally spec'd in earlier revisions of `docs/design/rebuild-plan.md` §8/§11.

**Why:** ecosystem-level decision — see workspace decisions at
`~/workspace/vedanta-dhobley/docs/decisions.md` 2026-07-01 entry for the
full rationale. NATS is designed as a broker; per-project brokers fight the
design center; cross-project consumption is trivial via a shared broker with
NATS account isolation.

**What stays vs what changes:**

- **Postgres `event_log` table stays** (per §3 schema). Still the durable
  audit trail. Every event is INSERTed into `event_log` for durability
  + backfill on SSE reconnect.
- **NATS is the fan-out mechanism.** Every event ALSO gets
  `nats.Publish(subject, payload)` for realtime delivery to subscribers.
- **SSE handler** (§8): subscribes to NATS `event.>` and `fixture.>`
  subjects on the found-footy account instead of `LISTEN`ing on a
  Postgres channel. Forwards to browser SSE consumers unchanged.
- **Webhook delivery worker** (§8): consumes NATS via a durable JetStream
  consumer with automatic replay on restart. Replaces the SQL `FOR UPDATE
  SKIP LOCKED` polling loop.

**Subject scheme (found-footy account):**

- `event.detected`, `event.stable`, `event.video_ready`,
  `event.rank_recalculated`, `event.removed`, `event.download_complete`
- `fixture.activated`, `fixture.completed`

Fully qualified via the found-footy NATS account:
`found-footy.event.video_ready` etc. — but subject strings within the
found-footy account use the short form.

**Rebuild plan impact:** §8/§9/§10/§11/§14 updated in the same commit as
this decision entry. `internal/infra/nats/` adapter added to §9;
`docker-compose.yml` no longer runs a per-project NATS in §10 (workspace
NATS is a dependency, not a project container).

---

## 2026-07-01 — Fresh rebuild in parallel, not incremental refactor

The rebuild happens as a **from-scratch build alongside the running
prod stack**, not as an in-place refactor of the existing code. Legacy
prod keeps serving traffic; new code stands up in parallel; cutover is
endpoint-by-endpoint until legacy has zero callers.

**Why**: [`design-audit.md`](design/design-audit.md) surfaced structural
gaps (data-layer god-class, embedded-arrays-vs-shared-primitive, no
tests, no deploy gate, `_event_id` overloaded five ways) that are
easier to fix by building fresh than by refactoring in place. The user
built the current system while learning documentation-driven agentic
development; the rebuild is the moment to apply everything learned
since. See also [`design-audit.md`](design/design-audit.md) §16
implementation order — the F-0..F-6 phases were incremental; this
rebuild subsumes them.

Codified in [`rebuild-plan.md`](design/rebuild-plan.md).

---

## 2026-07-01 — Postgres over Mongo (rebuild-context reversal)

The rebuild uses Postgres for structured data. This **reverses**
[`design-audit.md`](design/design-audit.md) §3's verdict ("keep Mongo"),
because the audit assumed *incremental refactor* of prod Mongo data.

**Why the framing change flipped it**: in a fresh-build context, the
backward-compat costs that made Mongo the pragmatic pick evaporate.
Postgres's native schema is stronger than Mongo's JSON Schema
validators (can't be disabled, can't run in "warn"), foreign keys
actually enforce relationships, transactions across rows are
first-class, and the extension ecosystem (`pgvector`, `pg_trgm`,
`TimescaleDB`, `pg_partman`) bolts on new capabilities without new
services. `$addToSet` idempotency becomes `INSERT ... ON CONFLICT DO
NOTHING`; embedded arrays become normalized tables with joins. In a
fresh build these are the correct shapes.

**When the audit still applies**: for the *legacy read-side compat*
layer (queries against pre-cutover `fixtures_completed` documents),
Mongo stays online read-only. New writes go to Postgres exclusively.

---

## 2026-07-01 — Garage over MinIO for blob storage

The rebuild uses Garage (Rust, ~50 MB, S3-compatible, filesystem-backed)
instead of MinIO for blob storage.

**Why**: MinIO Inc.'s "Aistor" rebrand has visibly de-featured the
community AGPL release (web console removed, replication features
behind commercial license, commit cadence slowed). Betting on
community MinIO long-term is a real risk. Garage is actively
maintained, low resource footprint, and its on-disk format is just
content-hashed files in a directory tree — no proprietary format,
`ls`-able, `rsync`-backupable. Per-project isolation (each project's
docker-compose runs its own Garage instance) preserves the workspace
pattern established by Temporal / Postgres / etc.

**Application impact**: S3-compatible API means `boto3` / `aioboto3`
client code is unchanged. Only `S3_ENDPOINT` in `.env` differs.

---

## 2026-07-01 — LLM endpoint abstracted; nexus swap is config-only

The application never knows which LLM inference server it's talking to.
All calls go through a client that reads `LLM_ENDPOINT_URL` from
environment. Model IDs are discovered from `/v1/models` at startup.
Request/response shapes conform to OpenAI's chat-completions API,
which llama.cpp (joi today) and nexus (est. end of 2026) both speak.

**Why**: nexus is under active development at `~/workspace/nexus/`
and will replace joi as found-footy's LLM endpoint when ready. The
switch must be a `.env` edit + container restart, not a code change.
This decision codifies the abstraction as a load-bearing invariant.

**Consequence for [`design-audit.md`](design/design-audit.md) §6**: the
Track-1 workspace LLM gateway proposal is deferred pending nexus's
eventual API surface. If nexus provides concurrency/priority/routing
built-in, the gateway is redundant. If nexus doesn't, a thin
found-footy-side traffic shaper (not a workspace-wide gateway) may
be needed. Decide after nexus lands.

---

## 2026-06-30 — Cross-doc linking via markdown, no `[[wiki-links]]`

Docs reference each other via markdown `[text](./path.md)` syntax;
no Obsidian-style `[[]]` wiki-links anywhere in the project.

**Why**: markdown links work in every rendering context (GitHub,
terminal, IDE, Obsidian's graph view). `[[]]` is Obsidian-only —
GitHub renders it as literal non-clickable brackets, IDEs treat it as
plain text, and it's ambiguous when filenames collide across project
vaults. The "knowledge graph" effect comes from richly interconnecting
docs, not the syntax — markdown achieves the same graph in Obsidian.

Codified in global AGENTS.md § Cross-doc linking; project-side
reminders in [AGENTS.md](../AGENTS.md) § Documentation and docstrings
and [docs/README.md](design/README.md) intake rules.

---

## 2026-06-30 — Brain-stack (Khoj + basic-memory MCP + Obsidian vault) deprecated

The `~/workspace/obsidian/` self-hosted second-brain stack was deleted
on 2026-06-30. Never had containers running in practice, the vault was
empty of notes, the only configured project symlink was legal-tender's
`docs/`.

**Why deprecated**: the conceptual goal (cross-project knowledge
access via interlinked markdown) is now achieved by
`~/workspace/vedanta-dhobley/` as the user-global hub plus each
project's `docs/` with rich markdown cross-linking — no separate
aggregation layer or MCP-write-to-vault tooling required.

Global AGENTS.md was cleaned of the stale Khoj / basic-memory MCP /
vault references the same day; legal-tender's six file references
queued for cleanup in `~/workspace/vedanta-dhobley/docs/todo.md`.

---

## 2026-05 — Caddy fronts all HTTP; host ports dropped

HTTP services (Temporal UI, Mongo UI, MinIO console, Twitter VNC) moved
from publishing host ports to being reached through the workspace `proxy`
Caddy via per-project hostnames. Per-project Caddyfile lives in
`~/workspace/proxy/caddy/caddy.d/found-footy.caddy`. Only host port kept
is dev Temporal gRPC `7233` for host-side dev clients.

**Why**: avoids port-allocation thrash across the ~5 personal projects on
the node, makes per-project URLs memorable
(`found-footy-prod-temporal-ui.<base-domain>` beats `localhost:3200`), and
unifies access path with how `joi` already serves `llama-small.joi`.

See: `deploy/INFRA-NOTES.md`, commits `c71dbe3` + `c9d1c77`.

---

## 2026-XX — LLM URL switched to Caddy hostname on joi

`LLAMA_URL` moved from `http://joi.<tailnet>.ts.net:3102` to
`http://llama-small.joi` (no port). joi runs its own Caddy that maps the
model-named subdomain to whatever llama.cpp port that model is currently on.

**Why**: stable URL across model swaps and port reassignments. Project
code no longer needs to know which port a given model uses.

---

## (pre-history) — Scoped deduplication by `timestamp_verified`

In `UploadWorkflow`, incoming videos are split by `timestamp_verified`
before perceptual-hash comparison: verified pool compares only against
verified S3 videos, unverified against unverified, both via parallel
`asyncio.gather()`.

**Why**: a verified goal clip and an unverified clip from a different
match moment can hash-match (same broadcast, same camera angles). Without
scoping, the verified clip could be replaced by the unverified one. In
prod this correctly blocked a Goal 1 clip from being replaced by a Goal 2
clip in the same match (expected ~31′, got 15′).

---

## (pre-history) — Workflow-ID arrays over counters

`_monitor_workflows`, `_download_workflows`, `_drop_workflows` are arrays
of workflow IDs, mutated via MongoDB `$addToSet`. The previous design
used plain integer counters incremented after work completed.

**Why**: idempotent (re-running the same workflow can't double-count),
auditable (you can see which workflows participated), failure-resistant
(a failed workflow simply doesn't register, so counts naturally stay low
without manual decrement).

---

## (pre-history) — `signal-with-start` for serialized `UploadWorkflow`

Each event has exactly one `UploadWorkflow` with deterministic ID
`upload-{event_id}`. `DownloadWorkflow`s queue videos onto it via
`signal-with-start`. The upload workflow processes signals FIFO and exits
after 5 min of idle.

**Why**: multiple parallel downloads completing simultaneously would race
on S3 dedup and produce duplicate uploads. Serializing per event
eliminates the race; deterministic ID + signal-with-start handles "is
there one already running?" without an explicit check.

---

## (pre-history) — Twitter alias resolution inside `TwitterWorkflow`

Originally there was a separate `RAGWorkflow` that resolved aliases then
fire-and-forgot into `TwitterWorkflow`. That double fire-and-forget chain
produced duplicate Twitter workflows when either side retried. Now
`TwitterWorkflow` does the cache lookup (or RAG fallback) inline at its
own startup.

**Why**: one workflow, one set of retry semantics, no orphan duplicates.
The `RAGWorkflow` is still around but only for pre-caching during
`IngestWorkflow`.

---

## (pre-history) — 5-collection MongoDB design with `fixtures_live` as overwrite buffer

`fixtures_staging` (upcoming) → `fixtures_active` (in-progress, enhanced
with our `_*` fields) → `fixtures_completed` (archive, 14-day retention).
`fixtures_live` is a temporary overwrite buffer for raw API data so we can
diff against the enhanced events in `fixtures_active` without destroying
our enhancements. `team_aliases` is a persistent RAG cache.

**Why**: the earlier single-collection-merge approach kept overwriting our
enhancement fields (`_monitor_workflows`, `_s3_videos`, etc.). The
5-collection design treats "raw API state" and "our enhanced state" as
two separate documents that we only ever compare, never merge in place.

---

## (pre-history) — Auto-scaling via dedicated scaler container

`docker compose up -d` brings up infrastructure + a single scaler service.
`worker` and `twitter` are marked `profiles: ["managed"]` so they aren't
started by default. The scaler reads Temporal queue depth + the MongoDB
active-goal count every 30 s and uses `docker compose up --scale` to
adjust replicas between 2 and 8.

**Why**: Python's GIL limits each worker to one CPU core; the workload is
spiky (zero goals one minute, eight simultaneous the next). Hand-tuning
replica count doesn't work — the scaler reacts in seconds.

---

## (pre-history) — Fire-and-forget child workflows with `ABANDON` parent close policy

`MonitorWorkflow → TwitterWorkflow` and `TwitterWorkflow → DownloadWorkflow`
both use `start_child_workflow(... parent_close_policy=ParentClosePolicy.ABANDON)`.

**Why**: parent workflows have short cycles (Monitor is every 30 s, Twitter
loops ~10 min) — child workflows must outlive them. ABANDON means parents
can complete and be GC'd while children continue independently. Without
this, children would be cancelled when parents exit and we'd lose work.

---

## (pre-history) — Heartbeat-based timeouts for long-running activities

Activities like `generate_video_hash` and `deduplicate_videos` use
`heartbeat_timeout` instead of (or in addition to) `start_to_close_timeout`.
They emit `activity.heartbeat(...)` between units of work — e.g., every 5
frames during hash generation, every video during S3 comparison.

**Why**: arbitrary execution timeouts kill long-but-progressing activities
on big videos. Heartbeats prove the activity is actually making progress —
Temporal only fails it if heartbeats stop, not if total time exceeds an
arbitrary deadline.
