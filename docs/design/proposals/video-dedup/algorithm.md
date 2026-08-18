# Video-dedup algorithm proposal

Historical-only rationale from the [video-dedup proposal index](./README.md).
The current implementation is documented in the
[EventWorkflow ledger](../../../orchestration/event.md).

## Why the redesign — what Python got wrong

1. **URL-as-identity is trivially bypassed.** Different Twitter
   accounts posting the same clip have different tweet URLs. Python
   sees them as distinct → downloads N copies of the same bytes → M
   `video_assets` rows for one true asset → wasted S3 storage,
   wasted validation compute, split share counts on the same clip.

2. **No cross-batch (S3) dedup.** Python dedups WITHIN a search batch
   (multiple candidates for the same event → collapse same-URL). It
   does NOT check against the existing S3 corpus. Result: even if
   fixture A's search found and stored clip X yesterday, fixture B
   downloading X today creates a duplicate S3 object + a fresh
   `video_assets` row. Multi-share against B's event never happens.

3. **Serial pipeline pays the download tax before checking dedup.**
   Python downloads → hashes → uploads. Every candidate pays the
   full download cost even if we've already stored the exact bytes.
   For popular clips this is 5-10× wasted bandwidth per match.

4. **No robustness to re-encoding.** Twitter re-encodes videos at
   upload. Same source clip reshared with a different encoder → new
   bytes → new content hash → Python treats as new. Human eye sees
   the same clip.

**Speed complaint** ("slow as fuck") probably compounds from #3 +
serial per-attempt download in Python's asyncio driver + heavy
per-frame Python-side hashing. The redesign addresses each.

## Semantic model

### Category axis — binary, not three-way

- **verified** — clip has a visible broadcast clock AND its minute matches the API-reported minute for this event. Highest confidence — provably THE goal.
- **unverified** — clip has NO visible clock. Kept as a lower-confidence source; ranked below verified in the same event, but still shown.
- **wrong-clock rejection** — clip has a visible clock BUT its minute disagrees with the API. **Hard-reject before persistence.** Not a stored category; the candidate exits the pipeline. Reasoning: a clip showing minute 34 when the API says minute 78 is provably a clip of a different match minute (or a different match entirely), not "just less confident."

Dedup is **category-scoped**: verified dedups only against verified; unverified against unverified. Ranking: verified above unverified regardless of popularity, popularity sorts within each pool.

### Dedup layers — cheap → expensive

Four layers, ordered by cost:

| Layer | Signal | Cost | Catches |
|---|---|---|---|
| **Metadata hard-filter** | duration, resolution, aspect ratio, framerate via ffprobe | milliseconds | Corrupt / off-spec clips: sub-3s, over-90s, sub-720p, non-broadcast aspect ratios (portrait / 4:3 / weird), sub-20fps |
| **URL** | Exact `tweet_url` match against `video_shares.tweet_url` | O(1) index lookup, zero bytes | Same tweet seen before, or same tweet referenced by two events |
| **Content hash** | SHA256 of downloaded bytes | One hash over bytes we already have on disk | Same bytes at different URLs (re-uploads, mirror accounts) |
| **Perceptual hash** | Per-frame pHash at **quarter-second intervals** → LSH bucket signature | Frame extraction + N per-frame hashes (most expensive local op) | Re-encodes, watermarks, minor crops of the same underlying clip |

Each layer runs at TWO scopes:

- **Batch (within one event's Video pipeline)** — collapse duplicates found in the current event's search results before doing more work on any of them. All duplicates within a batch contribute their popularity to the survivor.
- **S3 corpus (against previously stored assets)** — check if any candidate matches something we already have; if yes, multi-share the existing asset instead of re-uploading. Cross-event dedup is what Python is missing today.

### Cost hierarchy for LLM vs local ops

For sequencing decisions later in this doc, the real cost order is:

`metadata read < content hash < LLM vision call (~2 concurrent on joi) < perceptual frame hashing at quarter-second intervals`

Perceptual frame hashing is more expensive than an LLM call in wall-clock terms — 120 frames for a 30s clip × per-frame hash work. This inverts the naive intuition that "local is always cheaper than network." Pipeline stage order reflects this: LLM validation happens BEFORE perceptual hashing so we don't hash a clip we're about to discard.

> **Correction (2026-08-09, [decisions.md](../../../decisions.md)).** This cost ordering does NOT hold in the as-built Go — streamed-ffmpeg extraction + parallel `VideoWorkflow` children make hashing cheap and parallelized. Vision-before-perceptual-dedup is still required, but for **correctness**: a clip's verified/unverified category is unknown until vision, and dedup is category-scoped (§ below). Hashing stays in the child for every clip; only the dedup *decision* is post-vision. Net effect vs the old premise: vision runs on every md5-unique clip, not just perceptually-unique ones.

### Hard-filter thresholds (fixed 2026-07-16, refined third pass)

All values from ffprobe output. Configurable via env vars (with these defaults):

| Filter | Threshold | Source |
|---|---|---|
| Duration minimum | 3s | Python `MIN_VIDEO_DURATION` |
| Duration maximum | 90s | Python `MAX_VIDEO_DURATION` |
| Aspect ratio minimum | 1.75 | Python `MIN_ASPECT_RATIO` |
| Aspect ratio maximum | 1.80 | tightened from Python's 1.82 (centers 16:9=1.7777 in the band) |
| Short edge minimum | 600px | Python `MIN_SHORT_EDGE` — allows letterboxed 720p content |
| Framerate minimum | 20fps | new in Go — Python has no fps filter, broadcast is 24/25/30/50/60 |

**Evaluation order (short-circuit on first failure):**

1. **Duration** — most definitive; sub-3s = malformed/thumbnail loop, over-90s = compilation reel or half footage. Fails caught here don't burn log space on other reasons.
2. **Aspect ratio** — rejects portrait/mobile clips next-most-commonly.
3. **Framerate** — filters unusual encodes (animated GIFs re-encoded, low-fps upscales).
4. **Short edge** — last because letterbox semantics are fuzzier; earlier filters catch more definitive garbage first.

Short-circuit rather than collect-all-failures — simpler code, log-line-per-reject is enough for observability. Any candidate failing ANY of these exits the pipeline at Stage 3 before any hashing or vision work. No re-check of these downstream.

## Algorithm — the pipeline

The pipeline runs per event. Each of N Discovery-search DownloadWorkflows produces a batch of candidate URLs → downloads → local dedup → signals results to the per-event AssetWorkflow which serializes S3-affecting work. AssetWorkflow completes deterministically when all N Download batches have signaled AND its queue is empty (see § AssetWorkflow serialization + queue-drain completion below).

Each candidate URL flows through these stages:

**Stage 1 — URL check** (per candidate URL, before download).
```
SELECT video_asset_id FROM video_shares
WHERE tweet_url = candidate_url
LIMIT 1
```
Hit → INSERT a new `video_shares` row for the current event pointing at the existing asset. Zero download, zero hash. Miss → continue.

**Stage 2 — Download bytes.** Twitter service call. Failure → drop candidate.

**Stage 3 — Metadata hard-filter** (ffprobe read).
Reject candidate immediately if ANY of: duration < 3s, duration > 90s, height < 720, aspect ratio outside 16:9 tolerance band, framerate < 20fps. Free wins — cheapest actual work in the pipeline, kills obvious garbage before any hashing or vision.

**Stage 4 — Content hash + batch dedup + S3 corpus short-circuit.**
```
hash = sha256(bytes)

# Batch dedup: does another candidate in this batch have the same hash?
if hash exists in this-batch's downloads_by_hash:
    # Same bytes, different tweet. Popularity gets absorbed via video_shares
    # (each URL will produce its own video_share row against the same asset).
    downloads_by_hash[hash].urls.append(candidate_url)
    delete bytes
    return  # skip everything below; the batch-representative for this hash
            # will handle vision/perceptual/upload for the whole cluster.

# S3 corpus check: does this hash already exist in video_assets?
SELECT id FROM video_assets WHERE content_hash = hash
if hit:
    # We already own these bytes — already validated, already scored, already stored.
    INSERT INTO video_shares (video_asset_id=existing_id, event_id, tweet_url=candidate_url)
    delete bytes
    return  # skip all vision + perceptual + upload work.

# Truly new content — add to batch as a fresh representative.
downloads_by_hash[hash] = { bytes, urls: [candidate_url], is_verified: undecided }
```

**Key insight:** batch dedup + S3 dedup here means **the vision call in Stage 5 runs at most once per unique content hash**, not once per download attempt. If 4 tweets share the same bytes, Python's current code runs vision 4 times; we run it once. If we already own the bytes, we don't run vision at all.

**Stage 5 — Vision call #1: combined is-soccer + screen-recording + clock check.** Fires only on unique-in-batch, fresh-to-S3 representatives. Preserves Python's approach (`archive/src/activities/vision.py:552` `validate_video_is_soccer`) which is already a combined structured-JSON call covering three orthogonal concerns.

Frames sampled from the clip go to joi's Qwen3-VL-8B endpoint. Structured output (5 fields, mirroring Python's proven shape):
```json
{
  "soccer": true|false,
  "screen": true|false,
  "clock": "MM:SS" | null,
  "added": "+N" | null,
  "stoppage_clock": "MM:SS" | null
}
```

Field meanings — TIGHTENED from Python's current definitions (`vision.py:552` prompt shows Python's `soccer` accepts "highlights, celebrations, stadium recordings" which is what lets non-broadcast content through; Python's `screen` only catches phone-filming-TV, misses software screen recordings):

- `soccer` — **true ONLY if this is DIRECT BROADCAST FOOTAGE from a professional live soccer match camera source**. Includes: match play, official broadcaster-produced replays, VAR footage shown by the broadcast, **on-field player celebrations following a goal** (running to the corner flag, group hugs, signature reactions, dugout reactions — legitimate broadcast tail content of a goal clip). **Excludes:**
  - **Commentary / reaction / livestream videos** — person visible or audible discussing the game, even with game footage embedded (livestreamers, YouTube reaction channels)
  - **Fan compilations** — multiple goals from different matches edited together
  - **Fan-shot stadium footage from crowd angle** — phone recorded from the stands. **Rejected in initial ship** for the false-positive/false-negative-on-broadcast reason: user wants to eventually WELCOME MORE fan-shot arena content, but current filters (both Python and this proposal) let too much unrelated content through when the rubric widens. Get proper broadcast footage classification working correctly first, then expand this category in a follow-up. Documented here so it's not a permanent design choice; it's a "phase 1 of a larger content ambition."
  - **Studio content / press conferences / interviews / post-match panels**
  - **Other sports / graphics-only / logos**
  - When in doubt about commentary vs pure broadcast → **false** (prefer false-reject to false-accept — pollution of the S3 corpus is worse than losing one candidate for one event).
- `screen` — **true if this footage is a recording of any screen by any means**. Includes:
  - Phone/camera filming a TV (moiré patterns, visible TV bezel, screen glare, tilted angle, visible room/furniture) — Python's existing coverage
  - **Software screen recordings** (visible window chrome, mouse cursor, browser tabs, OS taskbar, timeline scrubbers, video player controls) — NEW; Python misses this
  - When in doubt about direct broadcast vs any capture-of-capture → **false** (same corpus-pollution rationale as `soccer`).
- `clock` — MM:SS reading of the main broadcast clock, or null if not visible. Clock stops at 45:00 (halftime) and 90:00 (full-time).
- `added` — "+N" text if an added-time indicator is visible ("+3", "+7", etc.), or null.
- `stoppage_clock` — MM:SS reading of the SECOND clock that appears during added time (counts up from 45:00 or 90:00), or null. Python handles this two-clock case properly.

**Two-checkpoint verification** (preserved from Python — `vision.py:745+762`): the same structured call runs TWICE per video, at 25% and 75% frame positions. Both must agree on `soccer` and `screen` for `is_valid=true`. Doubles vision cost per unique clip but provides real robustness against transient bad frames or momentary graphics-only content.

Code applies decisions from the combined output:
- `soccer == false` (at either checkpoint) → **discard** (not a soccer broadcast).
- `screen == true` (at either checkpoint) → **discard** (phone-filming-TV, low quality + copyright-ambiguous).
- `soccer == true` AND `screen == false` AND some `clock` field mismatches API's minute for this event (accounting for the main + stoppage clock combination) → **discard** (wrong-clock rejection, provably a different match moment).
- `soccer == true` AND `screen == false` AND clock reading matches → mark `verified = true`.
- `soccer == true` AND `screen == false` AND no clock visible → mark `verified = false` (kept as unverified, lower confidence — no clock present is different from wrong-clock).

> **AS-BUILT (Stages 6–8) — see banner.** The perceptual *algorithm* below is
> right in spirit but the specifics diverged: as-built samples at **0.1 s** (not
> 0.25 s) into a binary per-frame `frame_hashes` sequence (not the
> `"dense:0.25:…"` text format); the match is a **30-frame `min_run` window
> tolerating 3 `max_gaps`** (`internal/domain/video/match.go` `video.Match`),
> which *replaces* Python's strict `min_consecutive=3`; dedup runs only over the
> current event's candidates (**no S3-corpus / cross-event lookup**); and there
> is **no LLM "Stage 8" quality call** — winner-selection is metadata-only
> (`video.IsUpgrade`), built but unwired (#171).

**Stage 6 — Perceptual frame hashing** (only for Stage-5 survivors). Preserve Python's algorithm verbatim (`archive/src/activities/hashing.py`):
- Extract frames every 0.25s via ffmpeg.
- **Histogram equalization** on each frame to normalize contrast/brightness (matters for videos with different color grading of the same underlying clip).
- Resize to 9x8 grayscale.
- **dHash** (difference hash) — compare adjacent pixels to build a 64-bit hash per frame.
- Storage format (preserve Python's text shape for cutover compat): `"dense:0.25:0.25=abc123,0.50=def456,0.75=..."`. Variable-length depending on clip duration.

**Stage 7 — Batch perceptual dedup + S3 perceptual lookup.** Preserve Python's offset-tolerant matching algorithm (`archive/src/utils/dedup_match.py` `_dense_hashes_match`). Within same category (verified vs unverified) only:
- For each candidate pair, iterate over every possible time offset between them.
- At each offset, count consecutive matching frames (per-frame Hamming distance ≤ `max_hamming=10`, timestamp tolerance = interval/2).
- If ≥ `min_consecutive=3` consecutive frames match at ANY offset → declare "same video."

The offset-tolerance is load-bearing: two clips of the same goal often start at different times (Clip A 5s pre-goal, Clip B 2s pre-goal). Signature-Hamming approaches would miss this because the same underlying frames land at different signature positions. Python's sliding-window offset search finds the alignment.

- **Batch:** run `_dense_hashes_match` pairwise over surviving representatives in the current event's batch. Below threshold → merge (URL lists combined into one cluster).
- **S3 corpus:** for each candidate, all-pairs `_dense_hashes_match` against `video_assets.perceptual_hash` within the same category filter. Hit → this candidate is perceptually the same clip as an existing asset. At corpus size ~thousands, all-pairs is tolerable; indexing (LSH-like structure over frame lists) is deferred to a future optimization phase.

Thresholds `max_hamming=10` per frame and `min_consecutive=3` are empirically tuned (Python's `MAX_HAMMING_DISTANCE` and `MIN_CONSECUTIVE_MATCHES` constants). Preserve as-is; re-tune only if V/d backfill surfaces false positives/negatives on real clusters.

**Stage 8 — Vision call #2: quality comparison** (only fires if 2+ candidates survive to this point in a single perceptual cluster).
Frames from all cluster members (including the existing S3 asset if one was matched in Stage 7) go into ONE call. Structured output ranks them:
```json
{
  "ranked_by_quality": [
    {"clip_index": 2, "score": 0.87, "notes": "sharpest, minimal macroblocking"},
    {"clip_index": 0, "score": 0.71, "notes": "..."},
    {"clip_index": 1, "score": 0.54, "notes": "heavy compression"}
  ]
}
```
Rationale: Python uses `(duration, file_size)` as a quality proxy — resolution alone lies (a 1080p heavy-compression clip looks worse than a clean 720p). LLM quality scoring on frames is more accurate.

Winner outcome:
- If winner is an EXISTING S3 asset (came in from Stage 7 corpus match) → losers become `video_shares` against it, delete losers' bytes.
- If winner is a NEW candidate AND an EXISTING S3 asset was in the cluster → **replace + absorb**: upload winner as a new `video_assets` row; migrate old asset's `video_shares` to new asset via `UPDATE video_shares SET video_asset_id = new_id WHERE video_asset_id = old_id`; delete old asset row; delete old asset's S3 object; other cluster members become shares against winner.
- If winner is a NEW candidate AND no S3 match → upload winner; other cluster members become shares against winner (perceptual duplicates within batch, correct multi-share).

**Stage 9 — Upload winner + insert `video_assets` + insert `video_shares`.**
Optimistic INSERT with `ON CONFLICT (content_hash) DO NOTHING RETURNING id` for the cross-event race (two events, two different tweet_urls, same bytes discovered concurrently). Winner uploads bytes; loser looks up winner's asset id and shares against it.

**End state:** every candidate URL from Discovery is accounted for by a `video_shares` row pointing at exactly one `video_asset` — either a freshly uploaded one, an existing corpus one (via URL, content-hash, or perceptual match), or the replacement of an existing corpus one that lost a quality-comparison.

### Popularity

Derived from `COUNT(video_shares) WHERE video_asset_id = X`, not stored as a counter.

- Every candidate that survives to Stage 9 (or hits Stage 1/4/7 shares) produces exactly one `video_shares` row.
- Batch dedup: N candidates with the same content hash → 1 asset row + N share rows → popularity = N.
- Cross-event multi-share: fixture B's tweet share against fixture A's asset → asset's popularity increments naturally.
- Replace + absorb: old asset's shares migrated to new asset → new asset inherits old asset's popularity, plus its own new shares.

No counter to keep in sync. Never drifts.

### AssetWorkflow serialization + queue-drain completion

Preserves Python's win (per-event upload serialization avoids intra-event races) but fixes Python's waste (5-min idle timeout on the tail of the last batch).

- **One AssetWorkflow per event_id.** Started via signal-with-start from the first DownloadWorkflow's batch. Deterministic workflow_id.
- **Signal delivery order = processing order.** Each DownloadWorkflow, on completion, sends `add_batch(batch)` including a `batch_index` (1..N, where N is currently 10). AssetWorkflow keeps a queue and processes signals FIFO.
- **Completion condition** (not idle-timeout):
  ```
  batches_seen = set()
  queue = deque()

  while True:
      wait for either:
          (a) signal → batches_seen.add(batch.index); queue.append(batch)
          (b) hard-cap timeout (~30 min from workflow start; safety net only,
              covers the case where a DownloadWorkflow crashed silently)

      if queue is non-empty:
          process one batch  # single-threaded S3/pg mutation, per Python

      if len(batches_seen) == N and len(queue) == 0:
          # All expected batches have arrived AND we've drained them.
          exit immediately.
  ```
  Zero idle waste. Exit is deterministic when the last batch is processed.
- **Different events run different AssetWorkflow instances in parallel.** No cross-event coupling.

### Cross-event race handling

The only race the per-event serialization does NOT handle is: two different events (different AssetWorkflows) discover the same clip concurrently. Rare in practice but real. Resolution:
- Both compute the same content hash.
- Both try `INSERT INTO video_assets (content_hash, ...) ON CONFLICT DO NOTHING RETURNING id`.
- One wins → uploads bytes.
- Loser gets empty RETURNING → looks up winner's asset id → shares against it → deletes local bytes.

If both had already started S3 upload before the pg race resolved (very rare — requires them to both reach Stage 9 simultaneously): second upload is either idempotent (same key, same bytes) or a swallowable "key exists" error. Never incorrect, occasionally slightly wasteful.

### Text-based capture from day one (extensibility path)

Even though O3-V's primary consumer of `event_tweets` is video, the table **stores every tweet encountered during scroll**, not just video tweets. Overhead is negligible (~20-40ms extra per full 10-scroll search; ~500 bytes per text tweet stored). Rationale: future workflow types (sentiment analysis, text extraction, quote analysis, source-trust scoring) all read tweet text + author, and building the capture path from day one is trivially cheaper than adding a "backfill text tweets" migration later. See the schema below — `tweet_text`, `tweet_author`, `has_video` columns support text-based workflows without any schema migration; `processing` JSONB carries per-workflow-type outcomes as they land.

**Scroll-stop counter operates on the full set** of already-seen tweets (video or not), so the "3 consecutive already-seen → halt" heuristic works uniformly regardless of tweet type. Text-only tweets encountered in a previous attempt count toward the halt threshold on subsequent attempts, same as video tweets.

**Sentiment-analysis path (illustrative):** future `SentimentAnalysisWorkflow` reads `SELECT tweet_url, tweet_text FROM event_tweets WHERE event_id = $1 AND processing->'sentiment_analysis' IS NULL`, LLM-scores each, writes result into `processing.sentiment_analysis`. No schema migration needed. Similarly for text extraction, quote analysis, etc.

### Extensible tweet-processing tracking — `event_tweets` table

Python's "seen tweets" tracking is per-DownloadWorkflow in-memory state. Fine for video-only; doesn't extend to future non-video processing (sentiment, translation, transcript extraction, etc.). Proposed schema:

```sql
CREATE TABLE event_tweets (
    event_id       UUID NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    tweet_url      TEXT NOT NULL,
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- Captured at scroll time for future text-based workflows:
    tweet_text     TEXT,                -- from [data-testid='tweetText']
    tweet_author   TEXT,                -- @username, useful for source-trust filtering
    has_video      BOOLEAN NOT NULL DEFAULT FALSE,  -- fast filter for the video pipeline
    processing     JSONB NOT NULL DEFAULT '{}',    -- per-workflow-type outcome keys
    PRIMARY KEY (event_id, tweet_url)
);
CREATE INDEX event_tweets_scan       ON event_tweets (event_id, last_seen_at);
CREATE INDEX event_tweets_has_video  ON event_tweets (event_id, has_video) WHERE has_video = true;
CREATE INDEX event_tweets_pending_video
    ON event_tweets (event_id)
    WHERE has_video = true AND processing->>'video_download' IS NULL;
```

The two partial indexes target the two hottest query paths — video pipeline scan for pending downloads (video_download key not populated) and future text-workflow scans that need "all tweets for this event." The full-scan index over `(event_id, last_seen_at)` covers the scroll-stop lookup.

`processing` JSONB holds per-workflow-type outcome keys:
```json
{
  "video_download": {"status": "success", "at": "..."},
  "video_download": {"status": "filtered", "reason": "duration<3s", "at": "..."},
  "sentiment": {"score": 0.85, "at": "..."},
  "transcript": {"text_ref": "s3://...", "at": "..."}
}
```

Behavior:
- Every DownloadWorkflow batch, before scrolling, queries `SELECT tweet_url FROM event_tweets WHERE event_id = $1` into a set → skips those during scroll.
- Every new tweet encountered → INSERT ON CONFLICT DO UPDATE `last_seen_at` + write into `processing` under this workflow-type's key.
- Scroll-stop heuristic (Python's "we've hit already-seen tweets, stop"): configurable N-consecutive-already-seen tweets before halting.
- Per-event scope is deliberate: fixture B's search should not skip a tweet fixture A processed.
- Extensibility: any new workflow-type appends its outcome under a new key in `processing`. No schema migration per new type.

## What's decided going in

| Decision | Source |
|---|---|
| Dedup identity is `content_hash` (SHA256 of raw bytes), NOT tweet URL. Same clip at different URLs collapses into one asset. | 2026-07-16 Q4 sign-off |
| Multi-share against existing S3 assets. When ANY layer's check hits an existing asset, the current event's URL(s) become `video_shares` rows pointing at that asset, NOT new uploads. | 2026-07-16 Q4 sign-off |
| Cheap check first at every layer: metadata → URL → content hash → perceptual, both within batch and against S3 corpus. | 2026-07-16 walkthrough |
| Content hash algorithm: SHA256. Fast (Go stdlib native), collision-resistant to the point of overkill for our corpus size. | Default; not user-signed |
| Schema stays as-is: `video_assets.content_hash` UNIQUE, `video_assets.perceptual_hash` BYTEA (full signature), `video_assets.perceptual_hash_prefix` TEXT indexed for LSH. | Already in `schema.sql` from prior migration |
| Concurrency safety on `video_assets` inserts: optimistic `ON CONFLICT DO NOTHING RETURNING id`; loser looks up winner's id and shares. | Standard pg idempotency pattern |
| Cutover backfill: existing S3 corpus gets its assets' `content_hash` + `perceptual_hash` populated by a one-shot backfill activity before cutover, so Stages 4 + 7 lookups have data to match against. Bonus cleanup: catches Python-era duplicate S3 objects during backfill. | Migration requirement |
| Category axis is BINARY: `verified` (has clock + matches) vs `unverified` (no clock). Wrong-clock is HARD-REJECT before persistence, not a third category. | 2026-07-16 walkthrough |
| Dedup is category-scoped: verified only against verified; unverified only against unverified. Ranking: verified above unverified regardless of popularity; popularity sorts within pool. | 2026-07-16 walkthrough |
| Metadata hard-filter is Stage 3 (before hashing). Thresholds: duration 3s–90s, resolution ≥720p, aspect ratio 16:9 with small tolerance, framerate ≥20fps. Env-tunable but treated as fixed. | 2026-07-16 walkthrough |
| Perceptual hashing samples at quarter-second frame intervals (4 fps). Per-frame pHash 64 bits → concatenated signature (e.g. 7680 bits for a 30s clip). LSH prefix derived from a subset for bucket indexing. | 2026-07-16 walkthrough |
| Perceptual hashing is the MOST EXPENSIVE local op — more expensive than a joi LLM call. Pipeline runs vision BEFORE perceptual hashing so discarded candidates never get hashed. | 2026-07-16 walkthrough |
| Two LLM vision calls per pipeline run, both with structured output + rubric-based prompts: (#1) combined is-soccer + clock check per candidate, one call per unique-content-hash cluster survivor; (#2) quality comparison across cluster members, multi-video input, only fires when 2+ perceptual survivors remain. | 2026-07-16 walkthrough |
| Vision call #1 rubric tightened: "broadcast-camera view of a live soccer match with visible pitch and players in play." Fixes Python's too-lenient current filter that lets celebration reels / meme edits through. | 2026-07-16 walkthrough |
| Vision call #2 replaces Python's `(duration, file_size)` quality proxy which lies for compressed high-resolution clips. LLM scores actual perceived quality on frames. | 2026-07-16 walkthrough |
| AssetWorkflow is per-event, signal-based FIFO, single-threaded S3/pg mutations — preserves Python's serialization win. But completion is queue-drain-with-batch-count (exits when all N batches have signaled AND queue is empty), NOT idle-timeout — kills Python's 5-min tail waste. Hard-cap timeout remains as safety net for crashed DownloadWorkflows. | 2026-07-16 walkthrough |
| Cross-event race handled by pg `ON CONFLICT DO NOTHING RETURNING id` on `video_assets.content_hash`. Loser looks up winner's asset and shares. Per-event AssetWorkflow serialization handles intra-event; pg constraint handles cross-event. | 2026-07-16 walkthrough |
| Popularity is DERIVED from `COUNT(video_shares) WHERE video_asset_id = X`, not a stored counter. Batch dedup + cross-event multi-share + replace+absorb all reduce to correct share-row counts naturally, no counter to keep in sync. | 2026-07-16 walkthrough |
| Replace + absorb semantics: when a fresh candidate wins quality-comparison against an existing S3 asset (same perceptual signature, higher LLM quality score), OLD asset's `video_shares` migrate to NEW asset via `UPDATE video_shares SET video_asset_id = new_id`, OLD asset row deleted, OLD S3 object deleted. Shared consumers silently benefit from the quality upgrade — treated as a feature. | 2026-07-16 walkthrough |
| Extensible per-event tweet-processing tracking via `event_tweets` table with JSONB `processing` column keyed by workflow-type. Every workflow (video_download, future sentiment, future transcript, etc.) writes its outcome under its own key. No schema migration per new type. Scroll-stop heuristic reads this table. | 2026-07-16 walkthrough |
