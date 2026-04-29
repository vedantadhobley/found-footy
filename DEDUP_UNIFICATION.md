# Deduplication Unification: S3 Dedup → Multi-Video Matching + URL Rerouting

> **Status:** Planning document — implement after event matching feature is complete.

## Problem Statement

The **batch dedup** (comparing newly-downloaded videos against each other) can match one winner against an entire cluster of duplicates. The **S3 dedup** (comparing batch winners against existing S3 videos) only matches each new video against **the first matching S3 video** it finds, via a simple `break` on first match. This means:

1. A new higher-quality video can only replace **one** existing S3 video, even if it's a duplicate of **multiple** S3 videos.
2. Multiple lower-quality S3 entries that are duplicates of each other persist indefinitely.
3. Popularity from the un-matched S3 duplicates is never accumulated into the keeper.

Additionally, the current URL stability mechanism works by **reusing the old S3 key** when replacing a video. This is a 1:1 swap — if one new video replaces N old S3 videos, only one old URL survives. The other N-1 URLs break.

## Current Architecture

### Batch Dedup (Phase 1) — Works Correctly
**File:** [`src/activities/upload.py`](src/activities/upload.py#L471-L592) (`deduplicate_videos`, Phase 1 section)

- Union-find clustering: for each new video, scan all existing clusters for a perceptual hash match.
- One video can join a cluster with many members → **one winner replaces many**.
- Winner gets `total_popularity = sum(v.popularity for v in cluster)`.
- Selection uses `_pick_best_video_from_cluster()` ([L1162](src/activities/upload.py#L1162)): if durations are within 15% → pick largest file (better resolution); if durations differ >15% → pick longest.

### S3 Dedup (Phase 2) — Limited to First Match
**File:** [`src/activities/upload.py`](src/activities/upload.py#L594-L667) (`deduplicate_videos`, Phase 2 section)

The critical limitation is in this loop ([L623-L630](src/activities/upload.py#L623-L630)):

```python
matched_existing = None
for existing in existing_videos_list:
    existing_hash = existing.get("perceptual_hash", "")
    if existing_hash and _perceptual_hashes_match(perceptual_hash, existing_hash):
        matched_existing = existing
        break  # ← STOPS AT FIRST MATCH
```

After `break`, it compares the new video against only that one S3 video:
- If new is better → `videos_to_replace` (reuse old S3 key, overwrite file)
- If existing is better → `videos_to_bump_popularity` (just bump count)

**Other S3 videos that also match are never discovered.**

### URL Stability Mechanism (1:1 Only)
**File:** [`src/activities/upload.py`](src/activities/upload.py#L646-L651)

When a replacement is chosen, the old S3 key is passed through:
```python
file_info["_old_s3_key"] = matched_existing.get("_s3_key", "")
videos_to_replace.append({"new_video": file_info, "old_s3_video": matched_existing})
```

In `upload_single_video` ([L728-L733](src/activities/upload.py#L728-L733)):
```python
if existing_s3_key:
    s3_key = existing_s3_key  # Reuse old key → old URL still works
```

The workflow then does an **atomic in-place update** via `update_video_in_place` ([L818-L886](src/activities/upload.py#L818-L886)) so the MongoDB entry is updated without a gap where the video disappears.

This only handles **one** old URL. If the new video should replace 3 S3 videos, only one URL survives.

### S3 Key / URL Format
**File:** [`src/activities/upload.py`](src/activities/upload.py#L735-L743)

```python
# Format: {fixture_id}/{event_id}/{event_id}_{md5[:8]}.mp4
filename = f"{event_id}_{file_hash[:8]}.mp4"
s3_key = f"{fixture_id}/{event_id}/{filename}"
```

The URL stored in MongoDB is a proxy path: `/video/footy-videos/{s3_key}`  
(see [`src/data/s3_store.py`](src/data/s3_store.py#L147))

### Ranking
**File:** [`src/data/mongo_store.py`](src/data/mongo_store.py#L1156-L1224) (`recalculate_video_ranks`)

Sorts by: `(timestamp_verified DESC, popularity DESC, file_size DESC)`. Rank 1 = best.

### Popularity Bumping
**File:** [`src/data/mongo_store.py`](src/data/mongo_store.py#L1225-L1270) (`update_video_popularity`)

Updates a single video's popularity by URL, then recalculates ranks.

## Proposed Implementation

### 1. S3 Dedup: Match Against ALL S3 Videos (Cluster Approach)

Replace the first-match `break` pattern with a **cluster approach** identical to batch dedup.

**Change in `deduplicate_videos`** ([L594-L667](src/activities/upload.py#L594-L667)):

```python
# BEFORE: Find first match
matched_existing = None
for existing in existing_videos_list:
    if _perceptual_hashes_match(perceptual_hash, existing_hash):
        matched_existing = existing
        break

# AFTER: Find ALL matches → build cluster
matched_s3_videos = []
for existing in existing_videos_list:
    existing_hash = existing.get("perceptual_hash", "")
    if existing_hash and _perceptual_hashes_match(perceptual_hash, existing_hash):
        matched_s3_videos.append(existing)
```

Then treat `[file_info] + matched_s3_videos` as a single cluster:
- Pick the best video from the full cluster (new + all matching S3) using `_pick_best_video_from_cluster`.
- Accumulate popularity from **all** members.
- If the winner is the new video → replace the best S3 video's key (for URL stability), and **remove** the other matched S3 videos (their URLs get rerouted — see §2).
- If the winner is an existing S3 video → bump its popularity with the new video's count **plus** the popularity of any other S3 duplicates being consolidated, then remove the other S3 duplicates.

This fundamentally changes the return type — instead of a single `old_s3_video`, a replacement now involves **multiple** old S3 videos being consolidated.

### 2. URL Rerouting: `_video_redirects` MongoDB Field

Instead of only preserving one URL by reusing its S3 key, introduce a **redirect map** so that old URLs resolve to the surviving video.

**New MongoDB field on each event:**
```python
"_video_redirects": {
    "/video/footy-videos/123/event_abc/event_abc_deadbeef.mp4": "/video/footy-videos/123/event_abc/event_abc_cafebabe.mp4",
    "/video/footy-videos/123/event_abc/event_abc_12345678.mp4": "/video/footy-videos/123/event_abc/event_abc_cafebabe.mp4",
}
```

Each key is a **retired URL** and the value is the **surviving video's URL**.

**Changes needed:**

#### a) New model field
**File:** [`src/data/models.py`](src/data/models.py#L282) (`EventFields`)

Add:
```python
VIDEO_REDIRECTS = "_video_redirects"  # Dict[old_url → surviving_url]
```

#### b) When consolidating S3 duplicates
After picking the winner from the S3 cluster, for each **loser** S3 video:
1. Delete the S3 object (the file is being replaced by a better version or is a dup).
2. Add an entry to `_video_redirects`: `loser.url → winner.url`.
3. Remove the loser from `_s3_videos`.

#### c) Frontend video proxy
The frontend API that serves `/video/...` should check `_video_redirects` for the requested URL. If found, issue an HTTP 302 redirect to the surviving URL. This is a one-time lookup per request and can be cached.

#### d) Redirect chaining
If video A was redirected to B, and later B is replaced by C, update A's redirect to point to C (flatten the chain). This happens at consolidation time:
```python
# When retiring video B in favor of C:
for old_url, target_url in event["_video_redirects"].items():
    if target_url == b_url:
        event["_video_redirects"][old_url] = c_url  # A → C (not A → B → C)
event["_video_redirects"][b_url] = c_url
```

### 3. Popularity Accumulation

When a cluster of N S3 videos is consolidated into one winner:

```python
total_popularity = incoming_popularity  # From batch dedup
for s3_video in matched_s3_videos:
    total_popularity += s3_video.get("popularity", 1)
winner["popularity"] = total_popularity
```

This is already how batch dedup works ([L531](src/activities/upload.py#L531)). The S3 dedup currently only accumulates from the single matched video ([L643-L650](src/activities/upload.py#L643-L650)).

### 4. S3 Cleanup for Retired Videos

When retiring S3 duplicates, the actual S3 objects should be deleted since the redirect handles URL continuity. Currently `replace_s3_video` ([L888-L958](src/activities/upload.py#L888-L958)) handles single deletions. This needs to support batch deletion.

**New activity:** `retire_s3_videos`
```python
@activity.defn
async def retire_s3_videos(
    fixture_id: int,
    event_id: str,
    retired_videos: List[Dict],   # [{url, _s3_key, popularity}, ...]
    surviving_url: str,            # The URL they redirect to
) -> bool:
    # 1. Delete S3 objects for each retired video
    # 2. Remove from _s3_videos array
    # 3. Add entries to _video_redirects (old_url → surviving_url)
    # 4. Flatten any existing redirect chains
```

### 5. Workflow Changes

**File:** [`src/workflows/upload_workflow.py`](src/workflows/upload_workflow.py)

The `_process_batch` method needs to handle the new return shape from `deduplicate_videos`:

```python
# Old return shape:
{
    "videos_to_replace": [{"new_video": ..., "old_s3_video": ...}],  # 1:1
}

# New return shape:
{
    "videos_to_replace": [{"new_video": ..., "old_s3_videos": [...]}],  # 1:N
    "s3_consolidations": [{"winner_s3_video": ..., "retired_s3_videos": [...]}],  # NEW
}
```

`s3_consolidations` handles the case where the existing S3 video is already the best — no upload needed, but duplicate S3 entries still need to be retired with redirects.

## Summary of Files to Change

| File | Change | Scope |
|------|--------|-------|
| [`src/data/models.py`](src/data/models.py) | Add `VIDEO_REDIRECTS` field | Small |
| [`src/activities/upload.py`](src/activities/upload.py) | S3 dedup cluster matching, new `retire_s3_videos` activity, update return types | Large |
| [`src/workflows/upload_workflow.py`](src/workflows/upload_workflow.py) | Handle 1:N replacements + S3 consolidations, call `retire_s3_videos` | Medium |
| [`src/data/mongo_store.py`](src/data/mongo_store.py) | Methods for `_video_redirects` CRUD + chain flattening | Medium |
| [`src/worker.py`](src/worker.py) | Register `retire_s3_videos` activity | Trivial |
| Frontend API (external repo) | Check `_video_redirects`, return 302 if matched | Small |

## Edge Cases

1. **Race condition:** Two batches for the same event both find the same S3 duplicate. The serialized UploadWorkflow (signal-based FIFO queue) prevents this — only one batch processes at a time per event.

2. **Verified vs unverified scoping:** The workflow already splits dedup into verified/unverified pools ([L340-L345](src/workflows/upload_workflow.py#L340-L345)). The S3 cluster approach must respect this boundary — a verified video should never be consolidated with an unverified one.

3. **S3 key reuse vs new key:** When the new video wins and replaces multiple S3 videos, it should get a **fresh S3 key** (based on its own MD5). All old URLs (from retired videos) go into `_video_redirects`. This is simpler than choosing which old key to reuse.

4. **Existing single-video events:** If an event has only 1 S3 video and a new duplicate arrives, behavior is unchanged (1:1 match, same as current).
