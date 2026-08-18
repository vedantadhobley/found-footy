# Video-dedup delivery plan

Historical-only delivery detail from the
[video-dedup proposal index](./README.md). The sequence describes the proposal,
not the current implementation.

## Sequenced sub-commits

Total shape sits across O4 (Video pipeline — download, metadata filter, content hash, vision #1) and O5 (Asset pipeline — perceptual hash, corpus dedup, quality comparison, upload, share, per-event serialization).

Assumes T (Twitter port) has landed — Download activity relies on the ported service. Prior to T, tests use scenario-defined stubs.

### V/a — event_tweets table + URL check + Download activity (O4)

Schema: `event_tweets` table per the [extensible tweet-processing design](./algorithm.md). Backfill from Python's existing "seen tweets" state during cutover (empty at fresh start; `event_tweets` grows as new Discovery searches run).

VideoWorkflow input: `VideoInput{EventID, CandidateURL, ...}` — one workflow per candidate URL, spawned by Discovery's activity per the chain rule.

Activities:
- `URLCheck(url) → (found: bool, existing_asset_id)` — Stage 1 fast lookup against `video_shares.tweet_url`.
- `DownloadCandidate(url) → (bytes_location, err)` — Twitter service call.
- `RecordTweetProcessed(event_id, tweet_url, workflow_type, outcome)` — writes into `event_tweets.processing` under `video_download` key.

Wire into the chain: Discovery's activity spawns one VideoWorkflow per candidate URL. Discovery's `event_downstream_workflows` row completes when all its Video children settle. Each Video workflow inserts its own row on start, marks complete on exit.

~400 lines including tests.

### V/b — Metadata hard-filter + content hash + batch dedup + S3 short-circuit (O4)

Activities:
- `ExtractMetadata(bytes_location) → {duration, height, width, aspect, fps}` — ffprobe wrapper.
- `HardFilter(metadata) → (accept: bool, reason)` — applies 3s-90s / ≥720p / 16:9±tolerance / ≥20fps thresholds. Config-driven so we can tune without code changes.
- `ContentHash(bytes_location) → hash` — SHA256 stream over the file.
- `S3ContentHashLookup(hash) → (found: bool, existing_asset_id)` — Stage 4 corpus check.
- Batch dedup happens in VideoWorkflow orchestration logic (not a distinct activity) — tracks per-event downloads_by_hash across parallel VideoWorkflow siblings via a shared pg staging table `video_pipeline_batch(event_id, content_hash, first_workflow_id)` with `(event_id, content_hash)` UNIQUE. First to insert wins; losers see the winner's `first_workflow_id` and share against whatever asset the winner ultimately produces.

Batch dedup implementation note: Python does this in-workflow because it has one UploadWorkflow. Our Video-per-URL fan-out needs a pg-mediated batch state to avoid re-running vision + perceptual on candidates whose bytes already match another sibling's. This is the significant departure from Python — worth flagging as an implementation-detail decision to revisit if the pg staging table proves noisy.

~500 lines including tests.

### V/c — Vision call #1 (combined is-soccer + clock check) (O4)

Activity:
- `AnalyzeSoccerClip(frames_samples) → {is_soccer_broadcast, confidence, has_visible_clock, clock_minute}` — vision model call with structured output enforced by LLM adapter S6.
- Frame sampling for vision: fixed count (e.g. 6-8 frames uniformly sampled across the clip). Separate from perceptual hashing's quarter-second interval — vision doesn't need that granularity.

VideoWorkflow applies decisions from the output per the semantic model above. Fail (not soccer, or wrong clock) → mark `event_downstream_workflows` row complete with outcome_class, do NOT signal AssetWorkflow. Pass → signal AssetWorkflow with `(bytes_location, content_hash, event_id, urls, verified: bool)`.

Rubric prompt for the vision call lives in `internal/prompts/soccer_analysis.md` — versioned separately so it can be tuned without code changes.

~250 lines.

### V/d — AssetWorkflow scaffolding (per-event, signal-based, queue-drain completion) (O5)

`internal/workflow/asset.go` — one workflow instance per event_id. Started via signal-with-start pattern from the first VideoWorkflow to reach validation-pass.

Signal handler: `add_video_batch(batch)` — batch carries `{content_hash, bytes_location, verified, urls, download_workflow_index}`.

Main loop: queue-drain completion per the [AssetWorkflow serialization design](./algorithm.md). Exits when `batches_seen == N (10)` AND queue is empty. Hard-cap 30-min timeout as safety net for crashed DownloadWorkflows.

Wire into completion contract: AssetWorkflow inserts its own `event_downstream_workflows` row on start, marks complete on exit. Fixture completion (per 2026-07-11 contract) waits for it plus every VideoWorkflow.

~300 lines (workflow orchestration only; dedup activities land in V/e, V/f).

### V/e — Perceptual hash + LSH lookup + batch/S3 perceptual dedup (O5)

Activities:
- `PerceptualHash(bytes_location) → (signature, bucket_prefix)` — extract frames at quarter-second intervals, per-frame pHash, concatenate into full signature, derive LSH prefix.
- `S3PerceptualHashLookup(bucket_prefix, signature, threshold, verified) → (found: bool, matched_asset_ids: []UUID)` — LSH-narrowed candidates within same category pool, Hamming-verify against stored signatures.
- Batch perceptual dedup lives in AssetWorkflow orchestration logic (it holds all incoming batches serialized) — pairwise Hamming comparison across in-flight cluster members.

Storage: `video_assets.perceptual_hash` (BYTEA full signature) + `video_assets.perceptual_hash_prefix` (indexed TEXT LSH bucket key). Both already in schema.sql from prior migration.

~500 lines.

### V/f — Vision call #2 (quality comparison) (O5)

Activity:
- `CompareClipQuality(clips_frames_bundle) → {ranked: [{clip_index, score, notes}, ...]}` — multi-video vision call. Input is a bundle of frame-samples from each cluster member (fresh candidates + optionally the existing S3 asset's stored keyframes if one was in the cluster). Output is a ranked list with scores + reasoning.

AssetWorkflow orchestration picks the winner per the rules in Stage 8 of the algorithm (existing S3 asset wins → losers become shares; fresh candidate wins and existing S3 in cluster → replace + absorb; fresh candidate wins with no S3 in cluster → upload winner + shares).

Rubric prompt in `internal/prompts/quality_comparison.md`.

Only fires when 2+ candidates survived to this stage in a single perceptual cluster. Zero cost when singletons.

~300 lines.

### V/g — Upload winner + video_assets insert + video_shares insert + replace-and-absorb (O5)

Activities:
- `UploadNewAsset(bytes, content_hash, signature, bucket_prefix, verified) → asset_id` — optimistic pg insert with `ON CONFLICT (content_hash) DO NOTHING RETURNING id`, S3 PutObject on winner side, race-loser lookup on loser side.
- `MigrateShares(from_asset_id, to_asset_id)` — `UPDATE video_shares SET video_asset_id = to_asset_id WHERE video_asset_id = from_asset_id`. Used by replace + absorb.
- `DeleteAsset(asset_id)` — DELETE from video_assets + S3 DeleteObject. Used after MigrateShares in replace + absorb.
- `InsertShares(asset_id, event_id, urls[])` — bulk-inserts one row per URL against the target asset.

AssetWorkflow serializes these — the per-event FIFO ordering guarantees no intra-event race, and pg constraint handles cross-event.

~400 lines including tests.

### V/h — Cutover backfill

`cmd/backfill-video-assets/main.go` — one-shot binary that:
- Enumerates all S3 objects under Python-era prefixes
- For each object, downloads bytes, extracts metadata, computes SHA256 + perceptual hash + LSH prefix, calls Vision #1 for soccer + clock check (populates verified flag)
- Inserts `video_assets` row idempotently (dedup during backfill catches Python-era duplicate S3 objects — bonus cleanup; `MigrateShares` from duplicate to canonical)
- Runs during the migration window before cutover flips traffic

Empirical output from this run also produces the Hamming similarity threshold calibration data.

~500 lines.

### V/i — Cross-cutting: concurrency + failure modes + hard-cap timeouts

Documented in the [algorithm and coordination design](./algorithm.md). Implementation coverage lands here as tests, retry policies, hard-cap safety-net timers.

~200 lines of hardening.

## Deferred / not this proposal's scope

- **Twitter service port (phase T)** — VideoWorkflow's Download
  activity depends on the ported Twitter service to actually
  retrieve bytes. Until T lands, Download runs against a stub. T
  ships before O4 per [Q3 sign-off](../discovery.md).
- **Search-string RAG for Twitter queries** — separate concern
  addressed in T.
- **Destroy pipeline** (event.removed → cancel in-flight Video,
  soft-delete video_shares) — deferred to a follow-up phase after V.
- **Rank recalculation** — resolved 2026-07-18: **ranks are derived at read time via SQL window function**, no stored column, no rank-recalc activity, no `event.rank_recalculated` emit during normal flow. See [`decisions.md`](../../../decisions.md) 2026-07-18 entry. Fixes Python's rank=0 bug + concurrent-batch race window at the root.

## Resolved during 2026-07-16 walkthrough

**Second pass:**
- **Frame sampling** — quarter-second intervals (4 fps), not 8 uniform frames.
- **URL check scope** — indexed `video_shares.tweet_url`. Start simple; dedicated URL table if index cost surfaces later.
- **V/a vs V/c split boundary** — Video owns Stages 1-5 (URL + download + metadata + content hash + vision #1). Asset owns Stages 6-9 (perceptual + vision #2 + upload + share). Preserves existing phase boundary and independent evolution of the perceptual pipeline.
- **Cutover backfill scope** — full backfill of Python-era corpus. Manageable single-digit-thousands size; catches duplicate-object cleanup as a bonus.

**Third pass (after grepping Python's hash + match implementation):**
- **Perceptual hash algorithm** — **dHash** (9x8 grayscale, adjacent-pixel comparison, 64 bits/frame) with **histogram equalization** for lighting normalization. Preserve Python's algorithm verbatim (`archive/src/activities/hashing.py`). My earlier lean toward pHash was wrong on offset-tolerance grounds — see below.
- **Storage format** — Python's text form `"dense:0.25:t1=h1,t2=h2,..."` initially for cutover compat. Binary format is a post-cutover optimization; not blocking.
- **Matching algorithm** — Python's offset-tolerant sliding-window match (`_dense_hashes_match`): for every possible time offset between two videos, count consecutive per-frame matches (Hamming ≤ threshold, timestamp tolerance = interval/2); require ≥ N consecutive → same. The offset-tolerance is load-bearing (same-goal clips with different pre-goal buffer land the same underlying frames at different signature positions; signature-Hamming would miss). Preserve algorithm.
- **Similarity thresholds** — Python's `MAX_HAMMING_DISTANCE=10` per frame + `MIN_CONSECUTIVE_MATCHES=3` frames (0.75s at 0.25s sampling). Empirically tuned. Preserve. Re-tune only if V/d backfill surfaces false positives/negatives on real clusters.
- **LSH-style indexing** — dropped from initial scope. Python does all-pairs comparison against the corpus (O(N) per lookup, tolerable at ~thousands of assets). Deferred to a future optimization phase; when we need it we'll design an index structure suitable for variable-length frame lists (MinHash over frame hashes, representative-frame index, or similar) — LSH prefix on a fixed-size signature doesn't fit Python's variable-length frame-list shape.
- **Metadata hard-filter values + order** — reconciled with Python's actual config. Duration 3-90s (Python `MIN_VIDEO_DURATION` / `MAX_VIDEO_DURATION`), aspect band 1.75-1.80 (user's tightened centering of Python's 1.75-1.82), short edge ≥600px (Python `MIN_SHORT_EDGE`, allows letterboxed 720p content), framerate ≥20fps (new — Python has no fps filter). Evaluation order duration → aspect → framerate → short_edge, short-circuit on first fail. See the [algorithm thresholds](./algorithm.md).
- **Scroll-stop threshold via `event_tweets`** — **early stop on consecutive already-seen tweets in Twitter search scroll** — improvement over Python which uses exclude_urls only to skip individual tweets, not to stop the scroll. Python leaves real efficiency on the table for late attempts (7-10 out of 10) that walk through mostly-known tweets. Design: counter increments on each already-seen tweet, RESETS on each new tweet, stops scroll when counter reaches threshold. Default threshold: **3 consecutive**. Env-tunable. Discovery queries `event_tweets` before each search, passes URLs to T as `exclude_urls`; T's scroll loop uses them for both per-tweet skip AND the new early-stop. See `twitter-port.md` for the loop implementation.
- **Vision call #1 shape** — combined structured-JSON call preserving Python's `validate_video_is_soccer` (`archive/src/activities/vision.py:552`). 5 output fields: `soccer`, `screen`, `clock`, `added`, `stoppage_clock`. Two-checkpoint verification at 25% and 75% frame positions, both must agree on `soccer` and `screen`. **Rubric TIGHTENED significantly** from Python's version — `soccer` requires DIRECT BROADCAST FOOTAGE only (rejects commentary/reaction/livestream videos, fan compilations, fan-shot stadium footage from crowd angle, and Python's overly-permissive "highlights/celebrations/stadium recordings" allowance); `screen` EXPANDED to catch software screen recordings (window chrome, cursor, taskbar) in addition to Python's phone-filming-TV coverage. Both fields include "when in doubt → false" hedges (prefer false-reject to false-accept, since S3 corpus pollution is worse than losing one candidate per event). Doubles vision cost per unique clip (2 calls × unique-in-batch-and-fresh-to-S3 clusters) but provides real robustness against transient bad frames.
- **Vision call #2 shape** — new call (no Python precedent). One representative frame per clip (from ~50% timestamp) passed to Qwen3-VL-8B in a single multi-image call. Ranks clips 0-1 with one-sentence reasoning per clip. Hybrid rubric: prompt enumerates the dimensions to consider (sharpness, compression artifacts, color accuracy, motion smoothness, broadcast overlay clarity) but asks for a single overall score per clip, not per-dimension scores. Empirical tuning during V/d backfill using existing prod S3 corpus as calibration data. Upgrade to 3 frames per clip (25/50/75) if we see inconsistent rankings.

## Genuinely open

None blocking sign-off. Both vision-call rubrics (Stage 5 tightened, Stage 8 new) are marked for **empirical tuning during V/d backfill** — the initial rubrics are settled, but the prompts will be refined against the existing prod S3 corpus (which produces natural test cases: clips that survived Python's dedup, ready to be re-classified with the tightened `soccer`/`screen` rubric and quality-ranked in perceptual clusters).

V/a is unblocked (after O3/a-c and T ship).
