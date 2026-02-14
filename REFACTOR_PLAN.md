# Refactor: Validation & Deduplication Overhaul

**Branch:** `refactor/valid-deduplication`  
**Parent:** `feature/grafana-logging`  
**Started:** 2026-02-13

---

## Problem Statement

The current pipeline has two major quality issues:

1. **Deduplication is unreliable** — perceptual hash matching produces false positives (e.g. the Szoboszlai meme replacing a real goal clip) and the "longer = better" replacement logic makes it worse. Dedup needs to be **disabled** until we have a reliable verification layer.

2. **No timestamp verification** — we accept any video that "looks like soccer" but never verify it's showing the *right moment* in the match. A 30th-minute goal clip and a 75th-minute goal clip from the same match are indistinguishable to the current pipeline.

---

## Phase 1: AI Timestamp Extraction (NEW VERIFICATION LAYER)

### Concept

Broadcast soccer footage almost always displays a game clock (e.g. `30:00`, `47:12`). We can extract this timestamp from the frames we're already sending to the vision model, then compare it to the API-reported event time.

### How the game clock works

- Game clock format: `MM:SS` (e.g. `30:00`, `45:00`)
- Added time continues counting: `45+2` in the API = `47:xx` on the broadcast clock
- **The API reports the minute AFTER the goal happened.** So API time `elapsed=45, extra=2` (which we calculate as minute 47) means the goal actually occurred at minute **46:xx** in the broadcast clock.

### What we have from the API

From `event.time`:
- `elapsed`: int — The base minute (e.g. `45`)
- `extra`: int | None — Additional time (e.g. `2`)

The "broadcast minute" = `elapsed + (extra or 0)` — this is the minute as it would appear on the game clock, except the API reports +1 from when the goal actually happened.

So: **expected broadcast minute = elapsed + (extra or 0) - 1**

### Current AI validation flow

```
validate_video_is_soccer(file_path, event_id)
├── Extract frame at 25% of video duration
├── Extract frame at 75% of video duration  
├── Call vision LLM on each frame with prompt:
│   "Is this SOCCER? Is this a SCREEN recording?"
├── If 25% and 75% disagree → extract 50% frame as tiebreaker
└── Return: {is_valid, is_soccer, is_screen_recording, confidence}
```

Currently the function takes only `file_path` and `event_id`. The event minute is not passed down.

### Proposed change: Add timestamp extraction to vision calls

The 25% and 75% frames are the key ones for timestamp extraction — they represent early and late points in the video. The game clock should be visible in most broadcast frames.

**Updated prompt** — add a third question to the existing prompt:

```
3. CLOCK: What game time is shown on the broadcast clock/scoreboard?
   Look for a digital clock display showing MM:SS format (e.g. "34:12", "90:00+3")
   
   If you can see a game clock, report JUST the minutes as a number.
   Examples: "34:12" → CLOCK: 34
             "90:00" → CLOCK: 90
             "45:30+2" → CLOCK: 47
   
   If no clock is visible, answer: CLOCK: NONE

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO
CLOCK: <number> or NONE
```

### Timestamp validation logic

```python
def validate_timestamp(extracted_minutes: list[int | None], api_elapsed: int, api_extra: int | None) -> bool:
    """
    Check if any extracted game clock time matches the API-reported event time.
    
    Args:
        extracted_minutes: Clock minutes extracted from 25% and 75% frames (may be None if not visible)
        api_elapsed: API elapsed minute (e.g. 45)
        api_extra: API extra time (e.g. 2), or None
    
    Returns:
        True if at least one extracted time is within ±1 of the expected broadcast minute
    """
    # API reports +1 from when goal happened
    # Expected broadcast minute = elapsed + extra - 1
    expected = api_elapsed + (api_extra or 0) - 1
    
    for extracted in extracted_minutes:
        if extracted is None:
            continue
        if abs(extracted - expected) <= 1:
            return True
    
    return False
```

### What passes, what fails

| API time | Expected clock | Extracted clock | Result |
|----------|---------------|-----------------|--------|
| 45+2     | 46            | [45, 47]        | PASS (45 is within ±1 of 46) |
| 30       | 29            | [28, 30]        | PASS (28 is within ±1 of 29, 30 is within ±1 of 29) |
| 90+3     | 92            | [None, 91]      | PASS (91 is within ±1 of 92) |
| 30       | 29            | [65, 72]        | FAIL (wrong part of match) |
| 30       | 29            | [None, None]    | ??? (no clock visible — see below) |

### When no clock is visible

If neither the 25% nor 75% frame has a visible game clock, we **cannot verify** the timestamp. Options:
- **Option A: Pass anyway** — fail open, rely on soccer detection only (current behavior)
- **Option B: Soft fail** — mark as "unverified" but still allow, lower ranking priority
- **Option C: Hard fail** — reject videos with no visible clock

**Current recommendation: Option A (fail open)** — many valid clips crop out the scoreboard. We should not reject them, but we can use "clock verified" as a quality signal for ranking.

### Data flow changes needed

1. **`TwitterWorkflowInput`** — add `event_minute: int` and `event_extra: Optional[int]` fields
2. **`DownloadWorkflowInput`** — add `event_minute: int` and `event_extra: Optional[int]` fields  
3. **`validate_video_is_soccer()`** — add `event_minute: int` and `event_extra: Optional[int]` params
4. **`monitor_workflow.py`** — already has `minute` and `extra` in `twitter_triggered`, just needs to pass them through
5. **Vision prompt** — add CLOCK question
6. **Parse response** — extract CLOCK value
7. **Return value** — add `clock_verified: bool`, `extracted_minutes: list[int|None]`

---

## Phase 2: Timestamp-Bucketed Deduplication

### The problem with current dedup

The current perceptual hash dedup is **event-scoped** — it only compares videos within the same `event_id`. But it has no way to distinguish a clip of the actual goal from a clip of a different moment in the same match (e.g. a 30th-minute highlight vs the 75th-minute goal we actually want). Hash similarity between two broadcast clips of the same match is high regardless of which minute they show.

### The bucket strategy

Timestamp extraction from Phase 1 gives us a **verified game minute** for each video. We use this to sort videos into three trust buckets before dedup:

| Bucket | Criteria | Dedup behavior |
|--------|----------|----------------|
| **A: Verified match** | Extracted clock minute is within ±1 of expected API time `(elapsed + extra - 1)` | Dedup within this bucket only. These are confirmed clips of the right moment. |
| **B: Verified mismatch** | Extracted clock minute exists but is **outside** the ±1 range | **Reject entirely** — this is a clip of the wrong part of the match. Don't upload, don't dedup. |
| **C: No timestamp** | No clock visible in either the 25% or 75% frame | Dedup within this bucket only. Cannot verify, but may still be valid (cropped scoreboard, close-up replays, fan recordings). |

This is a **massive improvement** over the current approach because:
1. Bucket B clips (wrong minute) are rejected outright — they previously polluted dedup clusters and could replace correct clips
2. Bucket A and C are deduplicated independently — a verified 30th-minute clip can never match against a no-timestamp clip that might be from minute 75
3. S3 dedup gets the same benefit — when comparing against existing S3 videos, only compare within the same bucket

### How this applies to both dedup phases

#### Batch dedup (`deduplicate_videos` Phase 1)

Currently: all downloaded videos in a single event are compared against each other using perceptual hashes.

**New behavior:**
1. Assign each video to bucket A, B, or C based on its extracted timestamp
2. Discard bucket B entirely (log as `timestamp_rejected`)
3. Run perceptual hash clustering **only within bucket A** and **only within bucket C** separately
4. Select cluster winners from each bucket independently
5. Bucket A winners and bucket C winners both proceed to S3 dedup

#### S3 dedup (`deduplicate_videos` Phase 2)

Currently: batch winners are compared against all existing S3 videos for the event using perceptual hashes.

**New behavior:**
1. Each S3 video in MongoDB has a stored `timestamp_bucket` ("A", "C", or legacy "" for pre-migration videos)
2. Batch winners are only compared against S3 videos **in the same bucket**
3. A bucket-A winner is only compared to existing bucket-A S3 videos
4. A bucket-C winner is only compared to existing bucket-C S3 videos
5. Legacy S3 videos (no bucket) are treated as bucket C for comparison purposes

This prevents the exact scenario that caused the Szoboszlai bug: a meme video (which would be bucket C or B) replacing a verified goal clip (bucket A).

### Storage changes

The video object stored in MongoDB `_s3_videos` array currently has:
```
{url, perceptual_hash, resolution_score, file_size, popularity, rank}
```

**Add two new fields:**
```
{
  url, perceptual_hash, resolution_score, file_size, popularity, rank,
  timestamp_bucket: "A" | "C",    // Which bucket this video belongs to (B is never stored)
  extracted_minute: int | null     // The game clock minute extracted by AI (null if no clock)
}
```

These fields are also added to the video metadata flowing through the download → upload pipeline:
- `validate_video_is_soccer()` returns `extracted_minutes: [int|None, int|None]` and `clock_verified: bool`
- DownloadWorkflow attaches these to `video_info` dict before passing to UploadWorkflow
- UploadWorkflow includes them in the video object saved to MongoDB

**No schema migration needed** — existing S3 videos without `timestamp_bucket` are treated as bucket C (no timestamp info). New videos get the bucket assigned at validation time.

### The `video_info` dict through the pipeline

```
DownloadWorkflow
  ├── download_video()       → {file_path, file_hash, file_size, duration, ...}
  ├── validate_video()       → adds {clock_verified, extracted_minute, timestamp_bucket}
  ├── generate_hash()        → adds {perceptual_hash}
  └── signal UploadWorkflow  → full video_info with all fields

UploadWorkflow
  ├── deduplicate_by_md5()   → filters exact dupes (bucket-agnostic, MD5 is MD5)
  ├── deduplicate_videos()   → bucket-scoped perceptual hash dedup
  ├── upload_single_video()  → S3 upload with metadata
  └── save_video_objects()   → MongoDB with timestamp_bucket + extracted_minute
```

MD5 dedup stays bucket-agnostic because identical files are identical regardless of what minute they show.

### Fetch event data changes

`fetch_event_data()` in upload.py already loads `existing_s3_videos` from MongoDB with `perceptual_hash`. It needs to also load `timestamp_bucket` and `extracted_minute` so that `deduplicate_videos()` can scope S3 comparisons by bucket.

This is already done implicitly — `fetch_event_data()` loads the full video object from `_s3_videos`, so any new fields we store will automatically be available. No code change needed here.

---

## Implementation Order

1. ✅ Create branch `refactor/valid-deduplication`
2. ⬜ Add `event_minute` + `event_extra` to workflow inputs (TwitterWorkflowInput, DownloadWorkflow)
3. ⬜ Update vision prompt to extract game clock
4. ⬜ Add CLOCK parsing to `parse_response()`
5. ⬜ Add timestamp validation logic + bucket assignment
6. ⬜ Wire event_minute through monitor → twitter → download → validate
7. ⬜ Add `clock_verified`, `extracted_minute`, `timestamp_bucket` to video_info pipeline
8. ⬜ Update `deduplicate_videos()` to scope dedup by timestamp bucket (batch + S3)
9. ⬜ Store `timestamp_bucket` + `extracted_minute` in MongoDB video objects
10. ⬜ Test with live data
11. ⬜ Deploy and monitor clock extraction accuracy + bucket distribution

---

## Files to modify

| File | Changes |
|------|---------|
| `src/workflows/twitter_workflow.py` | Add `event_minute`, `event_extra` to `TwitterWorkflowInput` |
| `src/workflows/download_workflow.py` | Pass minute/extra to validate activity, attach bucket to video_info |
| `src/workflows/monitor_workflow.py` | Pass minute/extra to TwitterWorkflowInput |
| `src/activities/download.py` | Update prompt, add CLOCK parsing, timestamp validation, bucket assignment |
| `src/activities/upload.py` | Bucket-scoped dedup in `deduplicate_videos()`, reject bucket B, store new fields |
| `src/workflows/upload_workflow.py` | Include `timestamp_bucket` + `extracted_minute` in video objects saved to MongoDB |
