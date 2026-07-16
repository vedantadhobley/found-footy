# Video dedup redesign — design proposal (O4/O5)

**Status:** design-first draft. Do not implement anything from this
doc until it's reviewed + signed off.

**Cross-refs:**
- Plan intent — [`../../rebuild-plan.md`](../../rebuild-plan.md) §5 W4-W5 (VideoValidation + AssetPersistence)
- Prior decisions — [`../../decisions.md`](../../decisions.md):
  - 2026-07-16 Downstream workflow spawn via Temporal-direct + register-on-flip
  - 2026-07-11 Fixture completion contract via pluggable per-event workflow checklist
  - 2026-07-01 Workspace NATS as event bus + Garage / S3 migration
- Upstream — [`./discovery.md`](./discovery.md) (Q4 sign-off directs O4 kickoff to open with this doc)
- Python reference — `archive/python/...` (behavior baseline; NOT template)

## Purpose

Replace Python's URL-as-identity dedup — which misses same-clip-
different-URL cases entirely and does no cross-batch dedup against
the S3 corpus — with a layered pipeline that catches identical bytes,
identical content, and near-identical content. Cheap check first at
every layer. Multi-share against existing assets when a match hits at
any layer.

The pipeline lives inside the Video → Asset chain in the downstream
spawn model: Discovery spawns Video (download + validate); Video
spawns Asset (dedup + persist + share). Each stage owns its
`event_downstream_workflows` row per the [2026-07-16 spawn rule](../../decisions.md).

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

Three orthogonal dedup layers, cheap → expensive:

| Layer | Signal | Cost | Catches |
|---|---|---|---|
| **URL** | Exact `tweet_url` match against `video_shares.tweet_url` | O(1) index lookup, zero bytes | Same tweet seen before; same tweet referenced by two events |
| **Content hash** | SHA256 of downloaded bytes | One hash op per downloaded clip | Same bytes at different URLs (identical re-uploads, mirror accounts) |
| **Perceptual hash** | Per-frame perceptual hash → LSH bucket signature | Frame extraction + N per-frame hashes | Re-encodes, watermarks, minor crops of the same underlying video |

Each layer runs at TWO scopes:

- **Batch (within one event's Video pipeline)** — collapse duplicates found in the current event's search results before doing more work on any of them.
- **S3 corpus (against previously stored assets)** — check if any candidate matches something we already have; if yes, multi-share the existing asset instead of re-uploading.

## Algorithm — the pipeline

For each event's Video pipeline processing N candidate URLs from
Discovery's Twitter search:

**Stage 0 — URL check (per candidate URL, before download).**
```
For each candidate_url in N candidates:
    SELECT video_asset_id FROM video_shares
    WHERE tweet_url = candidate_url
    LIMIT 1

    If hit:
        INSERT INTO video_shares (video_asset_id, event_id,
                                   tweet_url, discovered_at)
        Skip to next candidate.  # zero download, zero hash.
```

Cheapest possible check. Catches same-clip-re-shared-by-same-account
and same-tweet-quoted-in-multiple-searches. Fast index lookup.

**Stage 1 — Download + content hash + batch dedup (per surviving candidate).**
```
downloads_by_hash = {}    # local to this Video workflow

For each surviving candidate_url:
    bytes = download(candidate_url)          # goes to disk
    if !bytes: skip                          # download failed
    hash = sha256(bytes)

    if hash in downloads_by_hash:
        # Same bytes as another candidate in this batch.
        # Discard this download; add candidate_url to shared list.
        downloads_by_hash[hash].urls.append(candidate_url)
        delete bytes
        continue

    downloads_by_hash[hash] = { bytes, url: candidate_url, urls: [candidate_url] }
```

Result: `downloads_by_hash` has one entry per unique content within
the batch, with the list of URLs that produced identical bytes.

**Stage 2 — S3 content-hash short-circuit (per unique-in-batch hash).**
```
For each hash, entry in downloads_by_hash:
    SELECT id FROM video_assets WHERE content_hash = hash

    If hit (existing_asset_id):
        For each url in entry.urls:
            INSERT INTO video_shares (video_asset_id: existing_asset_id,
                                       event_id, tweet_url: url,
                                       discovered_at)
        delete entry.bytes
        remove from downloads_by_hash
        continue

    # Miss — this hash is new to S3.
```

Same short-circuit as Stage 0 but at bytes level, not URL. Saves the
perceptual-hash cost AND the S3 upload cost for anything already
stored.

**Interleave optimization (recommended):** run Stages 1 + 2 as a
single pass — hash each download as it completes, immediately check
S3, discard bytes on hit. Never accumulate more than one unique
survivor's bytes before pruning known-owned ones. Cuts memory + disk
pressure for busy matches.

**Stage 3 — VideoValidation (AI vision against clock).**
The workflow O4 has always been named for. Validates that the clip's
broadcast clock matches the API's reported match minute. Runs only
on Stage-2 survivors — clips we don't already own.

Failure of validation → discard candidate (no perceptual hash, no
upload). Success → continue to Stage 4.

**Stage 4 — Perceptual hash + batch perceptual dedup (per validated
survivor).**
```
For each validated survivor in downloads_by_hash:
    signature = perceptual_hash(bytes)   # 8 sampled frames × 64-bit pHash
    bucket_prefix = signature[:PREFIX_BITS]  # LSH bucket key
    survivor.signature = signature
    survivor.bucket_prefix = bucket_prefix

# Batch perceptual dedup: pairwise-compare signatures within batch.
# Only relevant if 2+ candidates survived to this stage.
Merge any two survivors whose Hamming(signature) < THRESHOLD into one entry
(carry both URL lists forward).
```

Perceptual hash algorithm choice → open question. Frame sampling
strategy → open question. Threshold → open question. Store the full
signature in `video_assets.perceptual_hash` (BYTEA) and the LSH
bucket prefix in `video_assets.perceptual_hash_prefix` (indexed
TEXT).

**Stage 5 — S3 perceptual-hash lookup (per batch survivor).**
```
For each survivor in downloads_by_hash:
    # LSH bucket narrowing: fetch candidates in the same bucket.
    SELECT id, perceptual_hash FROM video_assets
    WHERE perceptual_hash_prefix = survivor.bucket_prefix

    For each candidate_asset from that query:
        If Hamming(survivor.signature, candidate_asset.perceptual_hash) < THRESHOLD:
            existing_asset_id = candidate_asset.id
            For each url in survivor.urls:
                INSERT INTO video_shares (video_asset_id: existing_asset_id,
                                           event_id, tweet_url: url,
                                           discovered_at)
            delete survivor.bytes
            remove from downloads_by_hash
            break
```

LSH bucket_prefix narrows the search from "all of S3" to
"perceptually-similar candidates." Then Hamming distance against the
full signature confirms.

**Stage 6 — Truly-new upload + video_assets insert + video_shares
insert (per remaining survivor).**
```
For each truly-new survivor in downloads_by_hash:
    s3_key = generate_key(survivor.hash)

    # Optimistic insert first — race handling below.
    INSERT INTO video_assets (content_hash, s3_key, perceptual_hash,
                              perceptual_hash_prefix, ...)
    VALUES (survivor.hash, s3_key, survivor.signature,
            survivor.bucket_prefix, ...)
    ON CONFLICT (content_hash) DO NOTHING
    RETURNING id AS new_asset_id

    If new_asset_id was populated:
        # We won the race. Upload bytes.
        s3.PutObject(s3_key, survivor.bytes)

    Else:
        # Another concurrent Video workflow already inserted this
        # hash. Look up their asset_id; don't upload.
        SELECT id AS existing_asset_id FROM video_assets
        WHERE content_hash = survivor.hash
        new_asset_id = existing_asset_id

    For each url in survivor.urls:
        INSERT INTO video_shares (video_asset_id: new_asset_id,
                                   event_id, tweet_url: url,
                                   discovered_at)
    delete survivor.bytes
```

**End state:** every candidate URL from Discovery is accounted for by
a `video_shares` row pointing at exactly one `video_asset` — either a
freshly uploaded one or an existing one.

## What's decided going in

| Decision | Source |
|---|---|
| Dedup identity is `content_hash` (SHA256 of raw bytes), NOT tweet URL. Same clip at different URLs collapses into one asset. | 2026-07-16 Q4 sign-off |
| Multi-share against existing S3 assets. When ANY layer's check hits an existing asset, the current event's URL(s) become `video_shares` rows pointing at that asset, NOT new uploads. | 2026-07-16 Q4 sign-off |
| Cheap check first at every layer: URL → content hash → perceptual hash, both within batch and against S3 corpus. | 2026-07-16 Q4 sign-off |
| Interleave optimization: S3 content-hash check runs during the batch pass, not as a separate stage, so already-owned bytes drop before any perceptual work. | 2026-07-16 Q4 sign-off |
| Content hash algorithm: SHA256. Fast (Go stdlib native), collision-resistant to the point of overkill for our corpus size. | Default; not user-signed |
| Schema stays as-is: `video_assets.content_hash` UNIQUE, `video_assets.perceptual_hash` BYTEA (full signature), `video_assets.perceptual_hash_prefix` TEXT indexed for LSH. | Already in `schema.sql` from prior migration |
| Concurrency safety on `video_assets` inserts: optimistic `ON CONFLICT DO NOTHING RETURNING id`; loser looks up winner's id and shares. | Standard pg idempotency pattern |
| Cutover backfill: existing S3 corpus gets its assets' `content_hash` + `perceptual_hash` populated by a one-shot backfill activity before cutover, so Stage 2 + Stage 5 lookups have data to match against. | Migration requirement |

## Sequenced sub-commits

Total shape sits across O4 (Video pipeline — download, hash, batch
dedup, validation) and O5 (Asset pipeline — perceptual hash,
S3-corpus dedup, upload, share).

### V/a — Content-hash pipeline (O4 scope)

`internal/workflow/video.go` — VideoWorkflow input: `VideoInput{EventID, CandidateURL, ...}` (one workflow per URL, spawned by Discovery's activity per the chain rule).

Activities:
- `URLCheck(url) → (found: bool, existing_asset_id)` — Stage 0 fast lookup.
- `DownloadCandidate(url) → bytes_location` — Twitter service call (stub-satisfied in O4; real bytes after T lands).
- `ContentHash(bytes_location) → hash` — SHA256 stream over the file.
- `S3ContentHashLookup(hash) → (found: bool, existing_asset_id)` — Stage 2.

Fires video_shares insert against existing asset on URL / S3 hit.
Returns "candidate is new, needs validation + perceptual dedup" or
"already owned, done."

Wire into the chain: Discovery's activity spawns one VideoWorkflow
per candidate URL. Discovery's `event_downstream_workflows` row
completes when all its Video children are settled. Each Video
workflow inserts its own row on start, marks complete on exit.

~500 lines including tests.

### V/b — VideoValidation activity (O4 scope)

Existing plan §5 W4 shape. VideoWorkflow calls a Validate activity
after Stage 2 miss:
- Vision model call against joi's Qwen3-VL-8B endpoint
- Extracts broadcast clock from clip; compares to API-reported minute
- Returns valid / invalid + rationale

Only Stage-2 misses hit this — clips we already own are trusted from
prior validation.

Validation fail → VideoWorkflow marks its row complete with
`outcome_class='rejected_validation'` and does not spawn Asset.
Validation pass → spawn AssetWorkflow with (bytes, content_hash,
event_id, urls).

~200 lines (leans on existing LLM adapter S6).

### V/c — Perceptual-hash pipeline (O5 scope)

`internal/workflow/asset.go` — AssetWorkflow. Called by Video on
validation-pass.

Activities:
- `PerceptualHash(bytes_location) → (signature, bucket_prefix)` —
  Stage 4. Frame extraction (ffmpeg or Go-native), per-frame pHash,
  signature assembly. Algorithm choice → open question.
- `S3PerceptualHashLookup(bucket_prefix, signature, threshold) → (found: bool, existing_asset_id)` —
  Stage 5. LSH-narrowed candidates + Hamming distance verification.
- `UploadNewAsset(bytes, content_hash, signature, bucket_prefix) → asset_id` —
  Stage 6. Optimistic INSERT + S3 PutObject + race-loser lookup.
- `InsertShares(asset_id, event_id, urls[])` — final step.

~500 lines including tests.

### V/d — Cutover backfill

`cmd/backfill-video-assets/main.go` — one-shot binary that:
- Enumerates all S3 objects under `video-shares/`
- For each object, downloads bytes, computes SHA256 + perceptual hash
- Inserts `video_assets` row if not already present (dedup during
  backfill catches Python-era duplicates too — bonus cleanup)
- Idempotent: safe to re-run

Runs during the migration window before cutover flips traffic. See
plan §13.

~300 lines.

### V/e — Cross-cutting: concurrency + failure modes

- **Optimistic pg inserts** with `ON CONFLICT DO NOTHING RETURNING`
  handle two-events-discover-same-clip races. Race loser looks up
  winner's asset id and inserts video_shares against it.
- **Race on S3 upload** — the losing side does NOT upload. If both
  sides had already started uploading before the pg race resolved
  (rare), the second upload is either a no-op (same key, same
  bytes) or gets a "key exists" error we can safely swallow.
- **Download failures** — Temporal activity retry handles transient.
  After max retries, VideoWorkflow marks row complete with
  `outcome_class='download_failed'`. Does not spawn Asset.
- **Perceptual-hash false positives** — LSH bucket hit narrows to
  candidates, Hamming distance verifies. Threshold too loose →
  merges distinct clips. Threshold too tight → misses re-encodes.
  Real-data tuning during backfill (which produces a natural
  duplicate set) informs the threshold before cutover.

## Deferred / not this proposal's scope

- **Twitter service port (phase T)** — VideoWorkflow's Download
  activity depends on the ported Twitter service to actually
  retrieve bytes. Until T lands, Download runs against a stub. T
  ships before O4 per [Q3 sign-off](./discovery.md).
- **Search-string RAG for Twitter queries** — separate concern
  addressed in T.
- **Destroy pipeline** (event.removed → cancel in-flight Video,
  soft-delete video_shares) — deferred to a follow-up phase after V.
- **Rank recalculation** (event.rank_recalculated NATS emit) — the
  ranking algorithm redesign is its own conversation.

## Open questions for review

1. **Perceptual hash algorithm.** pHash (DCT-based, robust to
   compression + scaling, ~1ms per frame in Go), dHash (difference
   hash, fastest, robust to brightness, slightly weaker on crops),
   or wHash (wavelet, most robust, slowest). My lean: **pHash** —
   best balance of robustness to Twitter's re-encoding + speed.
   Push back if you have a preference from Python-era experiments.

2. **Frame sampling strategy.** Options: (a) 8 frames at uniform
   time intervals (fast, deterministic, works well for typical goal
   clips 20-45s); (b) keyframe extraction only (variable count, more
   semantically meaningful, expensive to extract); (c) fixed rate
   like 1fps up to a cap. My lean: **(a) 8 uniform-interval frames**
   — deterministic, fast, sufficient for our clip length distribution.

3. **Signature format.** Options: (a) concatenate 8 × 64-bit
   pHashes → 512-bit signature, LSH prefix = first 16 bits; (b)
   average frame pHashes → single 64-bit signature; (c) MinHash the
   frame pHashes into a shorter signature. My lean: **(a)
   concatenate + LSH prefix** — highest fidelity, easy to reason
   about Hamming distance.

4. **Similarity threshold (Hamming distance cutoff for "same").**
   Needs real-data tuning during backfill. My placeholder: for a
   512-bit signature, threshold of 32-48 bits (6-10% of signature
   length) as starting point. Confirm empirically during V/d.

5. **URL check scope.** Options: (a) `video_shares.tweet_url`
   indexed lookup (simple; but video_shares grows fast — index cost);
   (b) dedicated `video_share_urls (tweet_url PK, video_asset_id
   FK)` narrow lookup table (extra table; faster lookups). My lean:
   **(a) indexed video_shares.tweet_url** — start simple, add a
   dedicated table if index cost becomes a problem.

6. **Where does dedup live in the workflow chain — V/a in Video, V/c
   in Asset, or all in one?** Current proposal splits: Video owns URL
   + content-hash + validation (natural — Video has the URL, has to
   download to validate), Asset owns perceptual + S3 upload + share
   (natural — Asset owns the final storage decision). Alternative:
   collapse V/c into V/a so Video handles everything. My lean: **keep
   the split** — Asset as a distinct workflow lets us evolve the
   perceptual pipeline independently and matches the existing plan
   phase boundary.

7. **Backfill scope for cutover.** Options: (a) full backfill of
   every video_asset in the Python-era corpus (thousands of objects,
   expensive); (b) backfill only assets referenced by non-completed
   events (smaller); (c) lazy backfill on first cross-reference
   (adds complexity to Stage 2/5 lookups). My lean: **(a) full
   backfill** — one-shot batch job during migration window, corpus
   size is manageable (single-digit thousands), simplest correctness
   story.

Sign off on the 7 questions above and V/a starts (after O3/a-c and T
ship).
