# Python video-processing behavior

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 6. Download / Video Validation Behavior

### Trigger

Spawned by **TwitterWorkflow** as fire-and-forget child workflow:
- **WorkflowID:** `download{attempt}-{team_clean}-{player_search}-{event_id}`
- **Parent close policy:** ABANDON (continues if parent dies)
- **ID reuse policy:** REJECT_DUPLICATE

Receives video list from TwitterWorkflow (0-5 videos).

**File:** `archive/src/workflows/twitter_workflow.py:479-506`

### Registration (Proof of Startup)

**First thing DownloadWorkflow does** (before any download):

```
register_download_workflow(fixture_id, event_id, workflow_id)
```

This adds the workflow ID to `_download_workflows` via `$addToSet`
(idempotent). If registration fails, the workflow doesn't proceed
but it **doesn't crash**; TwitterWorkflow will see count stay low
and retry on next attempt.

**File:** `archive/src/workflows/download_workflow.py:127-154`

### Download Pipeline

Pipeline stages (in order):

1. **Download videos** (parallel, per-video retry)
2. **MD5 batch dedup** (within this batch only)
3. **AI validation** (only MD5-unique videos)
4. **Perceptual hash generation** (only validated videos)
5. **Filter out videos without valid hash**
6. **Signal UploadWorkflow** (or skip if no videos)
7. **Cleanup** (if no videos to upload)
8. **Check and mark download complete** (finally block — runs on
   all exit paths)

**File:** `archive/src/workflows/download_workflow.py:97-633`

### Download Videos (Parallel)

For each video, DownloadWorkflow spawns an `asyncio` task:

```python
async def download_video(idx, video):
  try:
    result = execute_activity(
      download_activities.download_single_video,
      args=[video_url, idx, event_id, temp_dir, source_url],
      start_to_close_timeout=90s,
      retry_policy=RetryPolicy(max_attempts=3, backoff=2x)
    )
    return result
  except Exception:
    return {status: "failed", error: "..."}
```

**Temp directory:** `/tmp/found-footy/{event_id}_{run_id}` (run_id
from workflow instance, ensures uniqueness across replicas)

**Failure modes** (per-video, independent):
- **Video download error (403, timeout, etc.)** → logged, file
  deleted, count incremented
- **Filtered videos** (aspect ratio wrong, too short/long) → counted
  separately, file deleted
- **Multi-video tweets** → flattened into results (treated as N
  separate videos)

**File:** `archive/src/workflows/download_workflow.py:205-308`

### MD5 Batch Deduplication

After all downloads, DownloadWorkflow groups successful videos by
MD5 hash:

```
groups = {}
for video in successful:
  hash = video.file_hash
  if hash not in groups:
    groups[hash] = []
  groups[hash].append(video)

# For each group with multiple videos (MD5 duplicates):
for group in groups.values():
  if len(group) > 1:
    winner = max(group, key=lambda v: (v.resolution_score, v.file_size))
    winner.popularity = len(group)  # All copies contribute to popularity
    delete other files
```

This catches cases where Twitter has the exact same video file
linked from multiple tweets.

**Important:** This is **batch-level only**. S3 MD5 matching is
handled by UploadWorkflow (see §7).

**File:** `archive/src/workflows/download_workflow.py:310-344`

### AI Validation (Soccer Detection)

For each MD5-unique video, DownloadWorkflow calls:

```
validate_video_is_soccer(file_path, event_id, event_minute, event_extra)
```

This sends the video to **joi's Qwen3-VL model** with a structured
prompt requesting a JSON response:

- **soccer** (bool): Is this soccer footage?
- **screen** (bool): Is this a screen recording?
- **clock** (string): Extracted clock reading (e.g., "42:15")
- **added** (string): Stoppage minutes
- **stoppage_clock** (string): Stoppage-adjusted clock

**Timestamp verification:**
- If clock visible: extract OCR'd minute
- Compare to API elapsed minute (±1 minute tolerance to handle stoppage)
- If match: set `timestamp_verified = true`
- If mismatch: reject video (discard)

**Structured output:** Video is classified into:
- ✓ **Valid & verified** (soccer + clock matches)
- ✓ **Valid & unverified** (soccer + no clock / unverified clock)
- ✗ **Rejected** (not soccer, or wrong timestamp, or screen recording)

**Smart 2-3 check strategy:** Frames sampled at 25% and 75% of video
duration. If both agree, done (2 checks). If disagree, tiebreaker at
50% (3 checks). Activity heartbeat at each check to prevent Temporal
timeout.

**Concurrency:** Semaphore-limited to `LLM_CONCURRENCY_PER_WORKER=2`
in-flight requests per worker (matches joi's max_parallel=2 cap).

**Retry policy:** 4 attempts with exponential backoff (3s → 30s max).
If validation fails after retries, video is **FAIL-CLOSED**
(rejected, deleted).

**File:** `archive/src/workflows/download_workflow.py:345-439`

### Perceptual Hash Generation

For each validated video, DownloadWorkflow generates a **perceptual
hash** (dHash, dense sampling at 0.25s intervals):

```
generate_video_hash(file_path, duration)
  -> {perceptual_hash: "dense:0.25:t1=h1,t2=h2,...", ...}
```

**Algorithm** (dHash with histogram equalization):
- Dense sampling: frames every 0.25s
- Resize to 9×8 grayscale, apply `ImageOps.equalize()`
- Compare adjacent pixels → 64-bit hash per frame
- Format: `"dense:0.25:<ts1>=<hash1>,<ts2>=<hash2>,..."`

Used downstream for deduplication (Hamming distance < threshold =
duplicate).

**Parallel generation** (all at once, with heartbeat every 5 frames
for progress signaling).

**File:** `archive/src/workflows/download_workflow.py:440-505`

### Signal UploadWorkflow

After all validation, DownloadWorkflow **always signals
UploadWorkflow** (even if 0 videos):

```
queue_videos_for_upload(
  fixture_id, event_id,
  player_name, team_name,
  videos_to_upload,  // May be empty
  temp_dir,
  failures_by_class  // Phase 1 telemetry
)
```

This activity uses Temporal client's **signal-with-start pattern**:
- If no UploadWorkflow exists yet: **start one AND deliver signal in
  same call**
- If one exists: **enqueue the signal to the FIFO queue**

Signal name: `"add_videos"`. ID reuse policy: `ALLOW_DUPLICATE`
(handles late batches after a prior UploadWorkflow completed).

Temporal guarantees **signal ordering** = FIFO per event.

**File:** `archive/src/workflows/download_workflow.py:580-632`

### Completion Check (Finally Block)

DownloadWorkflow has a **try/finally block**. On exit (success,
exception, early return), it runs:

```
finally:
  check_and_mark_download_complete(fixture_id, event_id, threshold=10)
```

This activity:
- Counts entries in `_download_workflows` array
- If count >= 10: sets `_download_complete = true` (atomically,
  guarded by `$ne` to prevent concurrent races)
- If count < 10: no-op

This is **idempotent** — multiple DLWFs running this concurrently is
safe.

**Failsafe:** If every DLWF dies before reaching its finally block,
UploadWorkflow has an idle-timeout check that also runs this.

**File:** `archive/src/workflows/download_workflow.py:558-578`

---

## 7. Upload / Asset Persistence Behavior

### Trigger

Spawned by `queue_videos_for_upload()` activity (called from
DownloadWorkflow) using **signal-with-start pattern**:

- **First signal:** Starts UploadWorkflow with `UploadWorkflowInput`
  containing empty `videos` list
- **Subsequent signals:** Enqueued to FIFO signal queue (no new
  workflow spawn)
- **WorkflowID:** `upload-{event_id}` (one per event)

UploadWorkflow stays alive and processes batches until **idle
timeout** (5 minutes of no new signals).

**File:** `archive/src/workflows/upload_workflow.py:80-110`

### Signal Handling (FIFO Queue)

UploadWorkflow defines a signal handler:

```python
@workflow.signal
def add_videos(batch):
  self._pending_batches.append(batch)
```

Temporal guarantees signal **delivery order** = FIFO. UploadWorkflow
processes one batch at a time (sequential, no race conditions).

**File:** `archive/src/workflows/upload_workflow.py:66-78`

### Batch Processing Pipeline

For each batch:

1. **Fetch fresh S3 state** (existing videos for this event)
2. **Check event exists** (VAR check — abort if deleted)
3. **MD5 dedup** (check against existing S3 videos)
4. **Perceptual hash dedup** (scoped by timestamp verification status)
5. **Decide outcomes per video:**
   - **Upload** (new, no S3 match)
   - **Replace** (S3 match, new is better quality)
   - **Bump popularity** (S3 match, S3 is better, keep existing)
6. **Upload to S3 (parallel)**
7. **Update MongoDB** (new videos + in-place updates)
8. **Recalculate ranks**
9. **Notify frontend**

**File:** `archive/src/workflows/upload_workflow.py:168-679`

### Fetch S3 State

UploadWorkflow calls `fetch_event_data(fixture_id, event_id)`.
Returns **fresh S3 video metadata** for this event from MongoDB
`_s3_videos` array. This is the key to eliminating race conditions:
fetch S3 state inside the **serialized UploadWorkflow**, so no other
upload can modify it concurrently.

**File:** `archive/src/workflows/upload_workflow.py:202-242`

### MD5 Deduplication (Against S3)

UploadWorkflow calls `deduplicate_by_md5(downloaded_files,
existing_s3_videos)`. Returns:

```
{
  unique_videos: [...],
  md5_duplicates_removed: int,
  s3_exact_matches: [{video, s3_video, new_popularity}, ...],
  s3_replacements: [{new_video, old_s3_video}, ...]
}
```

**MD5 exact matches** (identical files) against S3 are handled via:
- **Exact match, new is better quality:** Replace (reuse same S3 key)
- **Exact match, S3 is better/same:** Bump popularity (keep existing)

**File:** `archive/src/activities/upload/dedup.py:46-228`

### Perceptual Hash Deduplication (Scoped by Timestamp)

UploadWorkflow splits videos into **2 pools** based on timestamp
verification status:

```
verified_videos = [v for v in candidates if v.timestamp_verified]
unverified_videos = [v for v in candidates if not v.timestamp_verified]

verified_s3 = [v for v in existing if v.timestamp_verified]
unverified_s3 = [v for v in existing if not v.timestamp_verified]

# Run dedup in parallel, verified vs verified, unverified vs unverified
verified_result = deduplicate_videos(verified_videos, verified_s3)
unverified_result = deduplicate_videos(unverified_videos, unverified_s3)
```

**Why scoped?** Prevents a verified goal clip from being replaced by
an unverified clip of a different broadcast moment (same match
timestamp = similar perceptual hashes).

**File:** `archive/src/workflows/upload_workflow.py:320-357`

### Perceptual Deduplication Algorithm

`deduplicate_videos()` uses **union-find clustering**:

1. **Batch clustering:** Compare each new video against all previous
   videos by perceptual hash
   - Match found: add to existing cluster
   - No match: create new cluster
2. **For each cluster:**
   - If 1 video: keep as-is
   - If N videos: **pick best using duration-aware selection**
     - If durations within 15%: prefer larger file (higher resolution)
     - If durations differ >15%: prefer longer (more complete clip)
     - Accumulate popularity from all cluster members to winner
3. **Batch winners vs. S3:**
   - If S3 match found: decide replace or skip (same duration logic)
   - If no S3 match: mark for upload

**File:** `archive/src/activities/upload/dedup.py:230-477`

### BUG? Multi-Match Corpus Dedup

At `archive/src/activities/upload/dedup.py:415-420`, the loop
searching for existing S3 videos to match a new candidate against
the corpus does:

```python
matched_existing = None
for existing in existing_videos_list:
    existing_hash = existing.get("perceptual_hash", "")
    if existing_hash and _perceptual_hashes_match(perceptual_hash, existing_hash):
        matched_existing = existing
        break  # ← STOPS AFTER FIRST MATCH
```

**Observed behavior**: if a new video is perceptually similar to N
existing S3 videos, only the FIRST match is processed. The other
N-1 stay in S3 as duplicates. `_old_s3_key` is only set for one
existing video, so upload replacement only touches one S3 key.

This is documented as behavior; the Go rewrite should decide whether
to preserve or fix.

### Three Outcomes Per Video

After dedup, each video is classified:

| Outcome | Action | S3 State |
| --- | --- | --- |
| **Upload** | New video → S3 PUT, add to `_s3_videos` | New entry |
| **Replace** | Upload new, keep same S3 key, in-place update metadata | Update existing |
| **Bump** | Keep existing, increment popularity, delete local file | Update popularity only |

**File:** `archive/src/workflows/upload_workflow.py:375-407`

### S3 Upload (Parallel)

For all videos marked upload/replace, `upload_single_video` is
called in parallel per video.

**S3 key format:** `{event_id}_{md5[:8]}.mp4`

**S3 URL:** `/video/footy-videos/{fixture_id}/{event_id}/{s3_key}`

**File metadata stored in S3 object tags** (via MinIO API, not
visible in object body).

**Retry policy:** 3 attempts with 1.5x backoff (2s, 3s, 4.5s).

**File:** `archive/src/workflows/upload_workflow.py:466-519`

### Scoring / Ranking System

`recalculate_video_ranks(fixture_id, event_id)` re-ranks
`_s3_videos` after each batch completes.

**Sort key:** `(timestamp_verified, popularity, file_size)` DESC.

Signal definitions:
- **`timestamp_verified`** (bool): Set by AI validation when the
  extracted clock matches the API's elapsed minute (±1 min). Primary
  tiebreaker.
- **`popularity`**: Count of duplicate sources found (how many
  Twitter accounts posted the same clip). Proxy for "the real clip"
  — many people posting it = probably real broadcast.
- **`file_size`**: Bytes. Proxy for resolution / quality.

No engagement metrics (views, likes, retweets) feed the score
today. No AI quality scoring beyond validation.

**Storage:** `rank` field (integer, 1..N) on each video object in
`_s3_videos` array.

**Winner determination** (used by fixture completion):
`monitor.py:417` checks `winner_exists =
completion_status["winner_exists"]` — checks if any video exists in
event's `_s3_videos` array. Rank #1 = winner.

**File:** `archive/src/data/videos.py:164-231`

### MongoDB Updates

**New videos:**

```
save_video_objects(fixture_id, event_id, video_objects)
  -> push to _s3_videos array
```

**Replacements (in-place):**

```
for replacement in replacements:
  update_video_in_place(
    fixture_id, event_id,
    s3_url,  # Same URL
    new_metadata  # Updated resolution_score, file_size, duration, hash, etc.
  )
  -> updates matching document in _s3_videos in-place
```

This avoids a race condition where video disappears between remove
+ add.

**File:** `archive/src/workflows/upload_workflow.py:556-634`

### Idle Timeout Failsafe

If UploadWorkflow gets **5 minutes with no new signals** (default
`UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES=5`), it exits and runs:

```
check_and_mark_download_complete(fixture_id, event_id, threshold=10)
```

This is a **failsafe** in case every DownloadWorkflow crashes
before reaching its finally block.

**File:** `archive/src/workflows/upload_workflow.py:94-109`

### Cleanup (File Level)

After successful S3 uploads, UploadWorkflow deletes the
**individual files** that were uploaded (not the entire temp
directory):

```
cleanup_individual_files(files_to_delete)
```

Temp directory is cleaned up by **MonitorWorkflow** after fixture
completes.

**File:** `archive/src/workflows/upload_workflow.py:681-712`

---
