# Design Audit — 2026-06-30

A holistic look at what found-footy is, what it does well, what it does
clumsily, and which clumsy parts are missing a *primitive* (vs which are
just legacy inertia or solved-but-undeployed). Companion to:

- [`audit.md`](./audit.md) — file-level correctness audit (May 2026,
  ~50 findings still mostly open). Asks "what's wrong at this line?"
- [`roadmap.md`](./roadmap.md) — the committed 7-phase rewrite plan.
  Asks "what do we ship this quarter?"

This audit asks **"is this the right shape?"** It's opinionated, and the
opinions are gated by lived evidence (incident references included), but
they're opinions, not findings. The point is to have something to argue
with before the rewrite phases run.

The recommendation at the end ([§16](#16-implementation-order))
reorganizes the roadmap given what this audit surfaces — it doesn't
replace it.

---

## 0. What's already working — don't touch (yet)

The fastest way to ruin a deep-pass rewrite is to "fix" the parts that
are already load-bearing-correct. Catalog of stuff that has earned its
keep:

- **5-collection (now 6) MongoDB split-of-concerns** (`fixtures_staging`,
  `fixtures_live`, `fixtures_active`, `fixtures_completed`,
  `team_aliases`, `top_flight_cache`). The `fixtures_live` overwrite
  buffer in particular is genuinely clever — a stable "snapshot of raw
  API state" we can diff against our enhanced `fixtures_active` without
  risking overwriting enhancement fields. Don't collapse this without a
  concrete win.
- **Workflow-ID-as-`$addToSet` idempotent counter.** Mongo array of
  workflow IDs + `len(array) >= threshold` check, instead of a counter
  field. Idempotent under Temporal retries and concurrent monitor
  cycles. This pattern is right.
- **Scoped dedup by `timestamp_verified`.** Verified clips only compare
  against verified S3 videos; unverified against unverified. Prevents
  the "Goal 1 clip got replaced by a Goal 2 clip with similar broadcast
  framing" failure mode that surfaced in prod. Keep.
- **Per-event `UploadWorkflow` serialization via `signal-with-start`.**
  One workflow per event with deterministic ID `upload-{event_id}`, fed
  by FIFO signals from concurrent DownloadWorkflows. Serializes S3
  dedup correctly without locks. The *primitive* is right; see [§4](#4-dedup-strategy-end-to-end)
  for whether the *scope* of serialization (per-event) is too narrow.
- **Fire-and-forget child workflows with `ABANDON` parent close.** The
  short-cycle MonitorWorkflow can't block on 10-min Twitter workflows;
  ABANDON makes lifecycle work without leaking workflow handles. Right.
- **The multi-stage download filter shape.** Cheap filters first (URL,
  duration, dimension), then medium (aspect ratio, MD5), then expensive
  (AI vision, perceptual hash). Recently tightened
  (aspect ratio `1.75-1.82` on 2026-06-30; snowflake-ID length
  validation in P2b). Shape is right; values are tunable.
- **Caddy-based hostname routing for HTTP services.** The 2026-05
  migration from host ports to `proxy` Caddy was right — see
  [`decisions.md`](./decisions.md). Don't revisit.
- **joi via Caddy hostname (`http://llama-small.joi`).** Stable across
  model swaps and port reassignments. Right.

Everything below this point is what's clumsy.

---

## 1. The build–deploy gap: prod ≠ main

**This is the load-bearing one.** Everything else in this audit assumes
"prod runs main." On 2026-06-30 we discovered prod was running images
~7 weeks behind main — Phase 1 telemetry (`ef16d2f`) and Phase 2a
cause-logging (`65164f1`) were committed but never deployed, and every
"why didn't this work" investigation since had been looking at code prod
wasn't executing.

**Current state.** Manual `docker compose build` + `up -d`. No CI, no
automated deploy, no signal of drift. The scaler manages replica counts
but is blind to image versions. Image rebuilds happen when a human
remembers.

**Lived problem.** Phase 1 (2026-05-22), Phase 2a (2026-05-22), and the
`events.py` drop-workflow fix (2026-05-31) all sat dirty / undeployed
until 2026-06-30. The Netherlands v Morocco zero-video match would have
been diagnosable in 2 minutes via the typed-error logs; instead it took
45 minutes of Loki archaeology plus a shell into the container to figure
out the running code didn't have the fix at all.

**The missing primitive.** A "prod is N commits behind main" signal,
plus a deploy hook trustworthy enough that humans don't have to think
about it.

**Proposed alternative.**

1. Worker startup banner emits a structured log line with `image_tag`,
   `git_sha`, `built_at`. Promtail ships it to Loki. A saved Grafana
   panel shows "currently-running commit SHA on prod workers vs main
   HEAD."
2. A `bin/deploy` script (or `make deploy`) that does
   `git pull && docker compose build worker twitter && docker compose up -d --no-deps --no-build`
   for prod, dev, or both. Same script invocable manually or from CI.
3. A GitHub Actions (later: self-hosted Forgejo-actions per the
   migration plan) workflow on push-to-main that posts a build-ready
   event to a webhook, which triggers (2) on the luv host. No registry
   needed — luv builds locally from its own checkout.

**Scope sketch.** M. Banner + script are S each; the poll-or-webhook
trigger is the M. Gating: webhook secret storage (lives in `.env`),
handling in-flight TwitterWorkflows during a worker recreate (verified
last night: Temporal replays them on the new worker).

**Why this is section 1.** Nothing else matters if prod silently
diverges from main again. The deploy-tracking panel should ship *before*
any other rewrite work begins — otherwise we'll re-litigate "is prod
actually running this fix?" every session.

---

## 2. Workflow ID conventions and identity

**Current design.** Three patterns coexist:

| Workflow         | ID pattern                                       | Stable? |
| ---------------- | ------------------------------------------------ | ------- |
| TwitterWorkflow  | `twitter-{event_id}`                             | yes (Sprint 1 stabilized) |
| DownloadWorkflow | `download{N}-{team}-{player}-{event_id}`         | **no** — `team` and `player` come from API and can mutate |
| UploadWorkflow   | `upload-{event_id}`                              | yes |
| RAGWorkflow      | `rag-{team_id}`                                  | yes |

**Lived problem.** When the API reassigns the goal scorer mid-match
(assist-to-goal flip, own-goal re-attribution), attempts 8+ get
different `team`/`player` slugs than 1-7. REJECT_DUPLICATE doesn't
catch this — different IDs aren't duplicates by ID. The Temporal UI
shows inconsistent names side-by-side that confuse anyone trying to
follow a single event's history. This is also the cosmetic complaint
from the 2026-06-30 conversation.

**The missing primitive.** A stamping convention that ALWAYS uses
`event_id` + stage + attempt-sequence, never API-mutable fields. The
constraint: *the workflow ID must be derivable from the event_id and a
stage-local counter alone.*

**Proposed alternative.**

```
twitter-{event_id}
download-{attempt:02d}-{event_id}     # was: download{N}-{team}-{player}-{event_id}
upload-{event_id}
rag-{team_id}                         # already stable
```

`event_id` itself encodes `{fixture}_{team}_{player}_{type}_{seq}` so it
carries human-readable identity. Temporal UI search finds workflows by
`event_id` substring regardless of stage.

**Scope sketch.** S. One line in
`src/workflows/twitter_workflow.py:473`. Rollout: ship the new format,
accept that in-flight TwitterWorkflows during the cutover might rarely
re-spawn a DownloadWorkflow that's already running with the old format
(REJECT_DUPLICATE doesn't catch cross-format dupes). Cheap, rare,
permanent fix.

**Bigger question (don't action).** Is `_event_id` (the Mongo key) the
same identity as the workflow-lineage root? Currently parallel
namespaces. Could collapse. Probably not worth it — the parallelism is
fine and Mongo-key concerns (uniqueness, indexing, querying) differ from
workflow-lifecycle concerns.

---

## 3. Data model — Mongo vs Postgres, plus schema discipline

You said you've been preferring Postgres lately. Honest read on whether
that fits this project:

**Current access patterns.**

| Pattern                                         | Frequency | Mongo today | Postgres alternative |
| ----------------------------------------------- | --------- | ----------- | -------------------- |
| Find fixture by ID                              | constant  | trivial     | trivial |
| Find events of a fixture                        | constant  | trivial (embedded) | one-join |
| Find videos of an event                         | constant  | trivial (embedded) | two-join or JSONB |
| Append workflow ID to event's tracking array    | constant  | `$addToSet` atomic | array column + `array_append` (atomic but verbose) |
| Cross-fixture: "find recent goals by player X"  | rare      | aggregate pipeline | clean SQL |
| Cross-fixture: "stats over fixtures by league"  | rare      | aggregate pipeline | clean SQL |

**Where Mongo earns its keep.** Embedded events + embedded `_s3_videos`
arrays + `$addToSet` idempotency. These are exactly the operations the
hot path runs every 30 seconds, and Mongo handles them natively. Doing
the equivalent in Postgres means JSONB columns (which is just Mongo
storage with less idiomatic access) or normalized tables (which makes
the hot-path queries multi-join).

**Where Mongo is biting.**

1. **No FK constraints.** `event._twitter_aliases` is a denormalized
   snapshot of `team_aliases.{team_id}.twitter_aliases` at the moment
   the TwitterWorkflow ran. If aliases get updated, the event's snapshot
   is stale. This isn't a bug — it's by-design — but the lack of *any*
   constraint means we can't tell whether a discrepancy is "expected
   denormalization" or "data corruption."
2. **No schema validation.** Mongo accepts whatever shape we write. The
   audit catalogued patterns where field names diverged
   (`_s3_videos` vs `s3_videos` — the MD5-dedup silent miss in
   `upload.py:352`). Mongo would have happily stored either.
3. **Event-ID uniqueness via string concatenation.**
   `f"{fixture}_{team}_{player}_{type}_{seq}"` is the primary key, but
   uniqueness is enforced only by sequence-number bookkeeping in code.
   If two MonitorWorkflows race on a new event detection, both could
   compute the same sequence and overwrite. Mongo doesn't help.
4. **47 `except Exception: return [] / False / None` patterns** in
   `mongo_store.py` history (per the May audit). Mongo's error surface
   is wide and we've been swallowing it everywhere.

**Verdict.** Keep Mongo. Migrating to Postgres would change every
access pattern in the codebase for benefits that don't materialize on
the hot path (where Mongo's embedded model is genuinely better) — only
on the cold reporting queries (where one-off SQL would be cleaner but
isn't the bottleneck).

**Invest in instead:**

- **JSON Schema validation on writes.** Mongo 7 supports
  [JSON Schema validators](https://www.mongodb.com/docs/manual/core/schema-validation/);
  declare them per-collection. Eliminates field-name drift bugs by
  rejecting bad writes at the database layer.
- **A typed Python layer (Pydantic) around document writes.** `mongo_store`
  methods take Pydantic models, not dicts. Pydantic emits the document
  in the validator-compatible shape. Field renames become type errors
  at the import boundary.
- **UUID-based `_event_id`.** Stop concatenating strings. Mint a
  `uuid.uuid4()` at first detection, store the
  `{fixture, team, player, type, seq}` tuple in a side-field for
  display. Eliminates the sequence-race risk and the
  "rebuild_event_id_if_player_changes" gymnastics.
- **Typed errors at the data layer**, not `except Exception`. Phase 1
  introduced typed errors at activity boundaries; push the same
  discipline down into `mongo_store` so swallowed Mongo errors become
  visible.

**Scope sketch.** M. JSON Schema validators are S per collection (×6).
Pydantic wrap is M (touches every write site). UUID event IDs are M
(touches `_event_id` everywhere — workflow IDs reference it, telemetry
keys it). Typed-Mongo-errors is S layered on Phase 1.

---

## 4. Dedup strategy end-to-end

**Current design.** Per-event scoped dedup (verified vs unverified
pools), parallelized via `asyncio.gather`. MD5 batch dedup for exact
matches. Perceptual hash with 16-bit Hamming threshold, 3-consecutive-
frame requirement at offset-tolerant comparison. UploadWorkflow
serialized per-event via `signal-with-start`.

**Lived problems.**

1. **`break` at first match in `upload.py:614`.** Documented in
   `todo.md`. If an event has 3 perceptually-equivalent S3 videos and a
   new one arrives, only the first match is replaced/popularity-bumped;
   the other two stay zombies. Design in
   `docs/proposals/dedup-unification.md`.
2. **No cross-event dedup.** Two goals scored ≤2 min apart by the same
   team can have Twitter clips that share the celebration / replay
   footage. We treat them as totally separate dedup namespaces, so the
   same clip can get uploaded twice — once per event. Rare in practice
   but real.
3. **Cross-instance dedup not expressible.** Per-event UploadWorkflow
   serialization handles within-event races, but two UploadWorkflows
   running concurrently for two events can both upload byte-identical
   clips. MD5 doesn't catch this because each is in its own scope.

**The missing primitive.** A **fixture-wide perceptual-hash index**
queryable from any UploadWorkflow, decoupled from per-event arrays.

**Proposed alternative.**

```python
# New collection: video_hashes
{
  _id: ObjectId(...),
  fixture_id: 1562345,
  perceptual_hash: "dense:0.25:0=ab12...",
  perceptual_hash_prefix: "ab12",       # for sharded similarity lookup
  canonical_s3_url: "http://minio:9000/footy/...",
  event_ids: ["1562345_40_234_Goal_1", "1562345_40_234_Goal_2"],  # multi-event when same clip
  popularity: 7,
  first_seen: <datetime>,
  ...
}
```

UploadWorkflow on intake:

1. Compute incoming clip's perceptual hash
2. Query `video_hashes` by `(fixture_id, perceptual_hash_prefix)` —
   small candidate set
3. Hamming-compare against candidates
4. If match: append `event_id` to existing record, bump popularity, link
   the event's `_s3_videos` array entry to the canonical record
5. If no match: insert new record, write to S3, link the event

The per-event `_s3_videos` array becomes a list of references to
`video_hashes._id`, not embedded copies. Cross-event dedup falls out for
free.

**Cross-instance dedup.** The query at step 2 catches concurrent
uploads if the first one commits first — but two simultaneous uploads
both at "incoming, hash matches nothing" can still race. Fix: use Mongo
unique index on `(fixture_id, perceptual_hash)` and treat
`DuplicateKeyError` on insert as "someone beat us, retry the lookup +
append path." Atomic.

**Tied to embedding migration.** Track 3 of the LLM stack redesign
proposal swaps perceptual hashing for image embeddings. If that ships,
this collection becomes a vector index (`fixture_id` + embedding
nearest-neighbor) instead of a hash index. Same *primitive*, different
math. Build the collection now with hashes; swap the matching math
later without restructuring.

**Scope sketch.** M alone, M+ combined with embedding migration. Worth
doing as a focused phase — it touches `UploadWorkflow`, every
`_s3_videos` access pattern in the data layer, the frontend
[Phase 6 plan](./roadmap.md) (URL stability under the indirection), and
the migration of existing `fixtures_completed` data into the new
collection.

---

## 5. Parallelism and concurrency

**Current state.** Workers run 30 concurrent activities + 10 concurrent
workflow tasks. Hash generation is **per-video** parallel
(`asyncio.gather` across the N discovered videos in a DownloadWorkflow)
but **per-frame serial** inside `_generate_perceptual_hash`. AI vision
calls are semaphore-bounded to 2 per worker process via
`LLM_SEMAPHORE = asyncio.Semaphore(2)` — but that's **per-process**, so
8 worker replicas × 2 = 16 concurrent vs joi's hard cap of 2.

**Lived problems.**

1. **joi LLM cap regularly exceeded.** Documented globally; biting
   harder during WC peaks.
2. **No priority.** CL night and R16 night compete equally for the
   discovery budget — no way to say "France-Norway is more important
   than this Conference League fixture."
3. **Scaler reacts, doesn't pre-empt.** Spawns more workers when queue
   depth grows, but doesn't know about *broadcast importance* — only
   "are there activities to do."
4. **Frame-level hash gen is the cheap win nobody took.** Each video's
   ~200-800 frames could be hashed in parallel within the activity
   (semaphore-bounded against memory). Currently serial means a 60-second
   video takes ~10s of hash gen wall-clock when it could take ~1s.

**The missing primitives.**

- A **global LLM concurrency gate** that knows about joi's hard cap
  regardless of replica count.
- A **frame-level worker pool** inside hash gen, gated by a memory-aware
  semaphore.
- **Per-fixture priority lanes** — separate Temporal task queues per
  priority level, scaler scales each independently.

**Proposed alternatives.**

1. **Ship Track 1 of `llm-stack-redesign.md` first.** Workspace LLM
   gateway in front of joi. Owns the global concurrency contract, kills
   the per-process Semaphores. Existing design — just ship it.
2. **Add a frame-level semaphore to `_generate_perceptual_hash`.**
   `asyncio.Semaphore(8)` (tunable) bounds concurrent frame extraction.
   Memory bound: ~50MB per frame in flight, so 8 frames ≈ 400MB ceiling
   per video. Safe.
3. **Defer priority lanes** until we observe contention under (1)+(2).
   Premature to design before the gateway exists.

**Scope sketch.** Track 1 is M (existing proposal). Frame-level pool is
S. Priority lanes XL — separate task queues, separate scaler logic,
separate workflow scheduling. Hold.

---

## 6. LLM concurrency and serving topology

**Current state.** Two activity types make LLM calls:
`download.validate_video_is_soccer` (vision) and `rag.get_team_aliases`
(text). Both endpoint `http://llama-small.joi`. Both use a per-process
`asyncio.Semaphore(2)`. joi's hard cap is 2 concurrent calls per the
host-budget rule.

**Lived problem.** With 8 worker replicas × 2 activities × 2 per-process
= up to 32 concurrent against a 2-cap. joi returns 503 / drops to a
crawl when over.

**The missing primitive.** A global concurrency gate that's aware of all
callers across the workspace, not just within one Python process.

**Proposed alternative.** Track 1 of
`docs/proposals/llm-stack-redesign.md` — workspace-level LLM gateway
container, fronted by Caddy, owns the concurrency contract. Each project
(found-footy, spin-cycle, future legal-tender) calls
`http://llm.<base-domain>/v1/chat/completions` instead of joi directly.
Gateway enforces global concurrency, priority headers, backpressure,
metrics. Kills the per-process Semaphores entirely.

**Bigger question.** Should priority be a header per request
(`X-Priority: high`) or a queue (separate gateway endpoints per
priority)? Header is simpler; queue gives Prometheus separation for
free. Lean header now, queue later if metrics confuse.

**Scope sketch.** M (existing proposal). Trivial removal of per-process
Semaphores after gateway lands.

---

## 7. Error taxonomy and recovery

**Current state.** Phase 1 typed errors LIVE as of 2026-06-30 night.
~6 error classes
(`VideoGeoRestrictedError`, `VideoNotAvailableError`, `LLMUnavailableError`,
etc.). Per-event `_telemetry` field. `underlying_error_class` log fields
on download failures.

**Still missing.**

1. **Dead-letter handling.** When all 10 download attempts fail
   (NL-Mar pattern), the event sits in `fixtures_completed` with 0
   videos. Nothing flags it for retry or human review. No
   `_dead_letter` collection.
2. **Per-error-class retry policy.** Today everything retries 3× with
   2× backoff regardless of error type. `VideoNotAvailableError` should
   retry 0× (tweet was deleted; permanent). `VideoGeoRestrictedError`
   should retry 0× on the same proxy but YES on a different proxy (if
   proxy pool ever ships). Currently we waste budget retrying permanent
   failures.
3. **Partial-completion semantics.** What if upload succeeds for 3 of 5
   videos in a batch? Currently the event might end with `_s3_videos`
   populated but `_download_complete=true` even though we didn't fully
   process the batch. No way to express "we did our best, here's what
   succeeded."

**The missing primitives.**

- A **`dead_letters` collection** keyed by `event_id` with the failure
  class and the original event payload.
- A **per-error-class retry decorator** on activities, gated by the
  Phase 1 typed errors.
- An **explicit per-batch outcome shape** on `_telemetry`
  (`batches: [{batch_id, attempted, succeeded, failed_by_class}]`).

**Proposed alternative.** Don't pre-design — observe a week of Phase 1
telemetry first. We finally have the data to know which error classes
dominate. After that:

- Add dead-letter collection (S)
- Add per-class retry table (S)
- Extend `_telemetry` for batch outcomes (S)

**Scope sketch.** S each, M total, gated on observation. Start
collecting failure-class distribution now via Loki dashboards.

---

## 8. Twitter scraping strategy

**Current state.** Persistent Firefox + Selenium per twitter container,
cookie reuse, idle CPU patched (2026-06-30). Auto-scaled 2-8 instances.
Search returns ALL videos within `max_age_minutes` window.

**Lived problems.**

1. **Status-ID snowflake truncation bug.** 13-digit IDs in scraped URLs,
   source unknown after 5 weeks. NL-Mar confirmed still active.
2. **Cookie expiry every few weeks.** Manual VNC re-auth via
   `found-footy-prod-twitter-vnc.<base-domain>`. No prediction — we
   find out when search returns 0.
3. **Phone-cam clips dominate discoveries** (NL-Mar: 7 of 7 discovered
   were portrait). Tightened aspect ratio on 2026-06-30 helps post-hoc;
   would be better to filter at search-time.
4. **DOM-selector fragility.** Canary workflow (P4b) catches breaks
   but only after-the-fact.

**The missing primitives.**

- **URL validation at extraction time** beyond length (P2b). Try a HEAD
  request? Parse the syndication response shape? Currently we hand off
  garbage URLs to download and burn an attempt.
- **Cookie health prediction.** Track auth-required signals from the
  search service; emit a "cookie quality degrading" log line before
  total failure.
- **Source filtering.** Verified accounts, known broadcaster accounts,
  high-engagement tweets — currently weighted equally with random
  rando tweets.

**Bigger question.** Switch to the official Twitter API v2? Basic tier
is $100/mo, 10K tweets/mo. Current volume (~178 goals × ~5 search rounds
× ~10 videos/round ≈ 9K interactions/mo) is just under basic tier. The
math:

| Path                     | Cost/mo | Reliability | Maintenance burden |
| ------------------------ | ------- | ----------- | ------------------ |
| Selenium scraping (now)  | $0 + CPU + manual re-auth | medium (DOM breaks) | high |
| Twitter API v2 basic     | $100    | high        | low                |
| Mixed (API + scrape fallback) | $100 | high        | medium             |

Hidden cost of scraping today: the Firefox CPU bleed we just patched,
the canary monitoring infrastructure, the cookie-VNC dance, the DOM
selector audit-trail. Easily a couple hours/month of operator overhead.
At $100/mo that's a clean trade.

**Proposed alternative.**

- **Tier 1 (now):** Ship the URL validation + source filtering. Cheap
  per-search wins.
- **Tier 2 (deep-pass):** Cookie health prediction.
- **Tier 3 (separate decision):** Twitter API v2 switch as its own
  proposal. Won't be cheap to implement but the operator-overhead
  payback is real.

**Scope sketch.** S for Tier 1, M for Tier 2, XL for Tier 3.

---

## 9. Observability, alerting, deploy visibility

**Current state.** Phase 1 telemetry just landed. Loki has logs.
`docs/logging.md` documents canonical queries. `monitor-loki:3100` is
the forensic endpoint. Grafana exists but found-footy doesn't have a
dedicated dashboard.

**Still missing.**

1. **Alerting.** No Prometheus alerts on coverage SLO drop. No PagerDuty
   / ntfy / Slack notifications. We find out things are bad when the
   user notices.
2. **Deploy tracking.** "Prod is on commit X, main is on commit Y" not
   visible anywhere — see [§1](#1-the-build-deploy-gap-prod--main).
3. **Saved dashboards.** `docs/logging.md` is the cookbook but every
   investigation re-types the queries. Should be `view dashboard` not
   `LogQL practice`.
4. **Per-match coverage SLO visibility.** Phase 4a shipped per-match
   coverage summary log lines, but they're not yet aggregated into a
   "this week's coverage rate by league" view.

**Missing primitives.**

- **Alerting rules in Prometheus/Loki** (separate from queries).
- **A `deploy_event` log line** emitted at worker startup with
  `image_tag`, `git_sha`, `built_at` (cross-references §1).
- **A `found-footy.json` Grafana dashboard** committed to the repo and
  importable.

**Proposed alternatives.**

1. Ship a baseline dashboard: rolling coverage SLO, error-class
   breakdown, joi LLM cap-exceeded rate, scaler decisions, deploy
   freshness. ~5 panels.
2. Emit `deploy_event` log line per §1.
3. Add 3-5 alerts: coverage SLO < 50% for 1h, error rate spike, joi
   503-rate, scaler stuck at MAX_INSTANCES for >10m, prod commit drift
   > 7 days from main.

**Scope sketch.** S each, M total. Foundational because the rest of the
audit's recommendations rely on "we can observe whether they worked."

---

## 10. Filter pushdown and pipeline ordering

**Current state.** Filtering happens at `src/activities/download.py`:
aspect ratio (just tightened), short-edge resolution, duration. AI
vision filters further (soccer/screen) post-download. Perceptual hash
runs post-AI.

**Pushdown opportunities.**

1. **Duration filter** runs in `download.py` after the syndication-API
   response, but `duration_seconds` is **already available in the
   Twitter search result** before the worker even sees the URL. Move
   the duration filter into `twitter/session.py` to drop garbage at
   discovery.
2. **URL-format pre-validation.** Snowflake length already validated
   (P2b). Could add: is the host x.com / twitter.com? Is the status ID
   numeric? Is the URL well-formed? Cheap rejects pre-download.
3. **Account-quality scoring.** Tweets by verified accounts /
   broadcaster accounts / accounts with > N followers are higher-EV
   candidates than random users. Could filter or rank at discovery.

**Proposed alternative.** Add a `_pre_filter_videos` step in
`twitter/session.py` that drops videos failing duration / URL-format
checks before returning the URL list to the worker. Saves a per-clip
activity round-trip.

**Scope sketch.** S for duration push-down + URL format checks. M for
account-quality scoring (needs taxonomy + tuning).

---

## 11. Cross-project boundary — vedanta-systems API

**Current state.** vedanta-systems direct-reads Mongo over the
`luv-prod` docker network via a Node Express service that knows the
found-footy schema. Worker calls `notify_frontend_refresh` for SSE
push. Phase 6 of the roadmap is the FastAPI cutover.

**What Phase 6 has right.** Replacing direct Mongo reads with a typed
HTTP contract. URL stability under future schema changes. Owning the
SSE endpoint server-side.

**What Phase 6 is missing.**

1. **No typed contract.** Plan is "FastAPI exposes endpoints" — but is
   there an OpenAPI spec? If yes, do we generate TS types from it for
   vedanta-systems? Currently the API and the frontend share knowledge
   via documentation, not code.
2. **No auth.** vedanta-systems is currently trust-the-network on
   `luv-prod`. Once Phase 6 lands and the API is hostnamed via Caddy
   (`found-footy.<base-domain>/api/v1/...`), should we add Authorization
   headers? Probably yes given the multi-project portal context.
3. **No event push.** SSE is pull-shaped — the client opens a stream,
   server pushes updates. Goal-video-ready could also be webhook-pushed
   (vedanta-systems exposes a webhook endpoint, found-footy posts when
   a video ships). Lower-latency than SSE on cold connections.
4. **No contract versioning.** Phase 6 plans `/api/v1/...` but no plan
   for what happens when the contract changes. Sunset window? Deprecate
   headers?

**Proposed alternative.** Take the Phase 6 budget and add:

- **OpenAPI spec with TS type generation.** Single command in the
  vedanta-systems CI pipeline regenerates types from the spec. Schema
  drift becomes a TS error.
- **Auth via Caddy.** Caddy already fronts everything; add a header
  check (`X-Internal-Token` from the vedanta-systems API) at the
  Caddyfile, not in FastAPI.
- **Webhook delivery for "video ready" events.** Keep SSE for the
  realtime stream, add webhook for durable delivery.
- **Versioning policy in `docs/api-contract.md`.** Already exists as a
  file; populate it with the contract rules.

**Scope sketch.** Phase 6 is currently budgeted L. These additions move
it to L+ but they're the right additions.

---

## 12. Testing strategy

**Current state.** ~5% coverage. `test_clock_parsing.py` and
`test_activity_registration.py` are real.
`tests/test_rag_pipeline.py` is broken at import (per May audit).

**Lived problems.**

1. Every refactor is risky because no safety net. The Phase 3 module
   splits shipped without per-module tests; we trusted the human
   review and prod observation.
2. NL-Mar diagnostic relied on production data because no synthetic
   harness exists. We can't reproduce a goal-detection-to-S3-upload
   path locally without watching a live match.
3. Regressions can ship undetected — the `events.py` drop-workflow fix
   was authored 2026-05-31 and presumably never exercised in test (the
   bug it fixes was a Mongo-API limitation that only surfaces against a
   live Mongo).

**Missing primitives.**

- A **synthetic fixture lifecycle harness**: `tests/synthetic/` with a
  `test_match_lifecycle.py` that runs the full pipeline against dev
  infra using a recorded fixture's API response + a recorded set of
  Twitter clips. Replays the whole flow.
- **Per-module unit tests** for the Phase 3 split modules
  (download/vision/hashing, upload/core/dedup/replacement, data/* mixins).
- **Integration tests** for download → upload signal flow, dedup
  scoping, completion marking.

**Proposed alternative.** Target 50% coverage by end of deep-pass (from
5%). Start with the synthetic harness — one well-tested end-to-end
scenario uncovers more bugs than 50 unit tests against trivial
functions.

**Scope sketch.** L. But pay-back is enormous — every subsequent change
is faster and safer. Best run *alongside* the other audit-driven phases,
not as a separate "now we write tests" sprint that nobody will start.

---

## 13. Code organization post-Phase 3

**Current state.** Phase 3 module splits landed:
- `download.py` → `download/core.py` + `vision.py` + `hashing.py`
- `upload.py` → `upload/core.py` + `upload/dedup.py` + `upload/replacement.py`
- `mongo_store.py` → `data/store.py` + per-domain mixins (`fixtures.py`,
  `events.py`, `videos.py`, `aliases.py`, `cache.py`)
- `twitter/session.py` → `session.py` + `scrape.py`

**Still feels wrong.**

1. **`data/events.py` is 660+ lines** and is the central nervous system.
   The mixin pattern means it has everything from CRUD to lifecycle
   marking to drop-workflow tracking to completion. Single file, single
   class, ownership scattered within it.
2. **`session.py` is still 1100+ lines** even after the scrape.py
   extraction. The browser-automation logic and the search-orchestration
   logic could split further.
3. **Activity file sizes vary wildly.** `twitter/twitter.py` 280 lines
   vs `monitor.py` 760+. The big ones are doing too much; the small
   ones are well-shaped.

**Missing primitive.** Clearer **ownership** — which file owns the
"event state machine" vs "event Mongo CRUD" vs "event lifecycle
decisions"? Currently all three live in `data/events.py` mixed together.

**Proposed alternative.** A Domain-Driven structure where each domain
has its own bundle:

```
src/domains/
  fixture/
    model.py            # Pydantic types
    store.py            # Mongo CRUD
    service.py          # business logic
    lifecycle.py        # state machine
  event/
    model.py
    store.py
    service.py
    lifecycle.py
  video/
    model.py
    store.py
    service.py          # (dedup math lives here)
  alias/                # team_aliases
  broadcast/            # cross-cuts; LLM-driven extraction etc.
```

Activities and workflows compose against these domain services rather
than calling `mongo_store` directly. Better testability, cleaner
ownership.

**Scope sketch.** L. A real organizational pass. Probably the natural
home for backfilling the docstring policy (touching every file anyway).
Should run after data-model work in §3 (UUID event IDs, Pydantic types)
because the domain models depend on those.

---

## 14. Configuration management

**Current state.** `src/utils/config.py` is a flat constants file. Phase
4 added `orchestration_config.py` and started a `dedup_config.py` split
but didn't finish. Some magic numbers still appear inline (e.g. workflow
timeouts in DownloadWorkflow).

**Lived problems.**

1. Magic numbers re-appear in code despite the configs.
2. No env-var overrides — config is code, so changing a value means a
   rebuild and deploy.
3. No per-environment differentiation beyond docker-compose differences.
4. No type checking — `MAX_HAMMING_DISTANCE = "10"` would silently
   become a string-comparison bug.

**Missing primitives.**

- **Layered config**: defaults in code + env-var overrides + optional
  per-env YAML.
- **Typed config**: Pydantic `BaseSettings`.

**Proposed alternative.** Collapse into a single `src/utils/settings.py`
using Pydantic `BaseSettings`. Support env overrides (`AR_MIN_RATIO=1.78`
overrides the default). Document via the schema. Print on worker startup
so we can see "this worker started with these values."

**Scope sketch.** S. Mechanical-but-touches-everything change. Best
done in tandem with §13.

---

## 15. Single-node assumption and capacity (deferred)

**Current state.** Everything on luv: Mongo, Postgres, MinIO, scaler,
workers, twitter, monitor stack. joi is separate but is single-purpose
LLM serving.

**Lived problems.** 100°C thermal events during CL/WC peaks. Memory
budget per host CLAUDE.md is ~63 GB steady, ~101 GB at peak. Scaler can
spawn 8 worker + 8 twitter replicas — each with their own Firefox
process pre-2026-06-30.

**Why this section is deferred.** Almost all the thermal pressure came
from the Firefox CPU bleed (patched 2026-06-30) and the joi
concurrency-cap exceeded (Track 1 will fix). Premature to design
multi-host before observing post-fix utilization.

**What to do.** Observe through one full WC week with the new state.
If thermal events recur, the candidate split is "twitter replicas
move to a second host" — they're the largest per-replica RAM consumers
and don't need access to the local Mongo/Postgres/MinIO. Workers must
stay on luv (network round-trip costs).

**Scope sketch.** Deferred. Re-evaluate post-Track 1 + dashboards.

---

## 16. Implementation order

Reorganizing the existing [roadmap](./roadmap.md) given this audit:

### Phase F-0: Foundation (must-do-first, ~3 days)

These are the unblocks. Nothing else matters without them.

- **[§1](#1-the-build-deploy-gap-prod--main)** Deploy tracking + bin/deploy script.
- **[§9](#9-observability-alerting-deploy-visibility)** Baseline Grafana dashboard + 5 alerts.
- Existing **roadmap Phase 1** (was committed but not deployed pre-2026-06-30; now deployed but
  observe data, refine taxonomies).
- Existing **roadmap Phase 2** (status-ID truncation root cause — this is overdue).

### Phase F-1: Identity and schema discipline (~3 days)

The data-correctness foundation for everything else.

- **[§2](#2-workflow-id-conventions-and-identity)** Workflow ID unification (S).
- **[§3](#3-data-model--mongo-vs-postgres-plus-schema-discipline)**
  JSON Schema validators + Pydantic write layer + UUID event IDs (M).
- **[§14](#14-configuration-management)** Pydantic BaseSettings config (S).

### Phase F-2: Concurrency contract (~2 days)

Off-loads the load-bearing scarcity (joi LLM cap) properly.

- **[§6](#6-llm-concurrency-and-serving-topology)** Track 1 workspace LLM gateway (M).
- **[§5](#5-parallelism-and-concurrency)** Frame-level hash gen pool (S).

### Phase F-3: Dedup re-architecture (~3 days)

The big functional improvement nobody has done yet.

- **[§4](#4-dedup-strategy-end-to-end)** Fixture-wide `video_hashes` collection (M).
- Resolves the `break`-at-first-match bug *and* enables cross-event /
  cross-instance dedup.
- Sets up Track 3 (embedding swap) as a future drop-in.

### Phase F-4: Discovery hardening (~2 days)

- **[§10](#10-filter-pushdown-and-pipeline-ordering)** Twitter-side
  duration + URL filtering (S).
- **[§8](#8-twitter-scraping-strategy)** Tier 1 (URL validation +
  source filtering) and Tier 2 (cookie health prediction).
- Defer Tier 3 (API switch) as separate decision.

### Phase F-5: Frontend boundary (~3-4 days)

- **[§11](#11-cross-project-boundary--vedanta-systems-api)** Phase 6 FastAPI
  + OpenAPI + TS gen + Caddy auth + webhook events.

### Phase F-6: Testing + organization (~3-5 days, alongside everything above)

- **[§12](#12-testing-strategy)** Synthetic fixture lifecycle harness +
  per-module unit tests as modules get touched.
- **[§13](#13-code-organization-post-phase-3)** Domain-driven re-org.
- Docstring backfill as part of (13) — touching every file anyway.

### Deferred

- **[§7](#7-error-taxonomy-and-recovery)** Dead-letter + retry policy
  refinement — observe Phase 1 data first.
- **[§15](#15-single-node-assumption-and-capacity-deferred)** Multi-host
  capacity planning — observe post-Track 1 first.

### Cross-cutting

- Backfill docstrings per the new policy as files get touched
  (don't do a separate docstring-only pass).
- Update `audit.md` (May 2026) entries as their items get closed by the
  phases above.
- Append architectural decisions to `decisions.md` as each phase ships.

### Honest total

~16-20 focused days of work, distributed across 6-8 weeks if working
part-time. The roadmap had this at 12-15 days; the audit added
~3-5 days of foundation work (F-0 + F-1 + F-2) that wasn't in the
original plan but addresses the biggest *operational* gaps, not just the
correctness ones.

The endpoint is a found-footy that:

- Self-reports when prod diverges from main (no more 7-week surprises).
- Has typed errors, typed config, typed Mongo writes, OpenAPI'd APIs.
- Deduplicates fixture-wide (not just per-event), with the math
  swappable to embeddings.
- Routes LLM calls through a global gateway with priority + concurrency.
- Discovers Twitter clips through cheaper filters, with cookie-health
  prediction.
- Surfaces a clean API contract to vedanta-systems, push + pull.
- Tests its own lifecycle in CI.
- Has docs that match the code that's running.

That's the "phenomenal" target.
