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

## Phase 2: Disable Perceptual Hash Deduplication

### What to disable

The perceptual hash comparison in `upload.py` → `deduplicate_videos()` that:
- Groups videos by hamming distance of perceptual hashes
- Replaces "shorter" videos with "longer" ones (the broken logic)
- Uses `MIN_CONSECUTIVE_MATCHES=3` which is too loose

### What to keep

- **MD5 deduplication** — exact file matches should still be deduplicated (this is reliable)
- **Perceptual hash generation** — keep generating hashes for future use / metrics
- **S3 upload** — all unique-by-MD5 videos should be uploaded

### How to disable

In `deduplicate_videos()`: skip the hamming distance comparison, just pass through all videos as "new". Keep the function signature and logging so we can re-enable later.

---

## Phase 3: Re-enable Smart Deduplication (FUTURE)

Once timestamp verification is working and we have data on clock extraction accuracy:

1. Use timestamp as primary grouping signal (videos showing same game minute)
2. Only compare perceptual hashes WITHIN the same timestamp group
3. Raise `MIN_CONSECUTIVE_MATCHES` threshold
4. Remove "longer = better" replacement logic
5. Consider content-aware ranking instead (broadcast quality, resolution, bitrate)

---

## Implementation Order

1. ✅ Create branch `refactor/valid-deduplication`
2. ⬜ Add `event_minute` + `event_extra` to workflow inputs (TwitterWorkflowInput, DownloadWorkflow)
3. ⬜ Update vision prompt to extract game clock
4. ⬜ Add CLOCK parsing to `parse_response()`
5. ⬜ Add timestamp validation logic
6. ⬜ Wire event_minute through monitor → twitter → download → validate
7. ⬜ Add `clock_verified` to validation return + store in video metadata
8. ⬜ Disable perceptual hash deduplication in `deduplicate_videos()`
9. ⬜ Test with live data
10. ⬜ Deploy and monitor clock extraction accuracy

---

## Files to modify

| File | Changes |
|------|---------|
| `src/workflows/twitter_workflow.py` | Add `event_minute`, `event_extra` to `TwitterWorkflowInput` |
| `src/workflows/download_workflow.py` | Pass minute/extra to validate activity |
| `src/workflows/monitor_workflow.py` | Pass minute/extra to TwitterWorkflowInput |
| `src/activities/download.py` | Update prompt, add CLOCK parsing, add timestamp validation |
| `src/activities/upload.py` | Disable perceptual hash comparison in `deduplicate_videos()` |
| `src/data/models.py` | Add clock_verified field if needed |
