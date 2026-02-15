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

**CRITICAL:** We do NOT ask the AI to parse/convert the time. Broadcast clock formats are wildly inconsistent, and we need deterministic parsing logic. Instead, we ask the AI to report **exactly what it sees** on screen, and WE parse it.

### Broadcast clock format complexity

Broadcasts display game time in many different formats:

| Format seen on screen | What it means | Absolute minute |
|----------------------|---------------|-----------------|
| `34:12` | Normal first half | 34 |
| `84:28` | Normal second half (full display) | 84 |
| `112:54` | Extra time (full display) | 112 |
| `2H 5:00` | Second half, 5 min in | 45 + 5 = 50 |
| `1H 35:00` | First half, 35 min in | 35 |
| `ET 04:04` | Extra time, 4 min in | 90 + 4 = 94 |
| `AET 04:04` | After extra time, 4 min in | 90 + 4 = 94 |
| `ET 15:00 +2:43` | Extra time with stoppage | 90 + 15 = 105 |
| `45+2:30` | First half stoppage time | 45 + 2 = 47 |
| `90+3:15` | Second half stoppage time | 90 + 3 = 93 |
| `105+2` | First ET period stoppage | 105 + 2 = 107 |
| `HT` | Half time (no clock) | Cannot determine |
| `FT` | Full time (no clock) | Cannot determine |

**Key insight:** The AI should report the RAW text it sees. Our code parses it.

**Updated prompt** — add a third question to the existing prompt:

```
3. CLOCK: What does the game clock/match timer show on the broadcast scoreboard?
   
   Report EXACTLY what you see on screen, including any text indicators.
   
   Examples of what you might see:
   - "34:12" (just time)
   - "2H 15:30" (half indicator + time)  
   - "ET 04:04" (extra time indicator + time)
   - "45+2:30" (stoppage time format)
   - "90:00+3" (alternative stoppage format)
   - "HT" or "FT" (half/full time, no clock visible)
   
   Copy the clock display exactly as shown. If no clock/timer is visible, answer NONE.

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO
CLOCK: <exact text from screen> or NONE
```

### Clock parsing logic (our code, not AI)

The key insight: some broadcasts show **relative** time (e.g., "ET 04:04" = 4 minutes into extra time) while others show **absolute** time (e.g., "ET 102:53" = 102nd minute, already includes the 90). We need to detect which format is being used.

**Heuristics for format detection:**
- If minutes >= 90 and we see "ET": already absolute, don't add 90
- If minutes < 30 and we see "ET": relative to ET start, add 90
- If minutes >= 45 and we see "2H": already absolute, don't add 45  
- If minutes < 45 and we see "2H": relative to 2H start, add 45
- Stoppage format like "45+2" is always explicit: 45 + 2 = 47

```python
import re

def parse_broadcast_clock(raw_clock: str | None) -> int | None:
    """
    Parse raw broadcast clock text into absolute match minute.
    
    SMART FORMAT DETECTION:
    - Detects whether time is relative (to period start) or absolute (from match start)
    - "ET 04:04" → 94 (4 mins into ET, so 90+4)
    - "ET 102:53" → 102 (already absolute, 102 >= 90 so don't add)
    - "2H 5:00" → 50 (5 mins into 2H, so 45+5)
    - "2H 67:00" → 67 (already absolute, 67 >= 45 so don't add)
    
    Handles all common broadcast formats:
    - Normal: "34:12" → 34
    - Full display: "84:28", "112:54" → 84, 112
    - Half indicators (relative): "1H 35:00" → 35, "2H 5:00" → 50
    - Half indicators (absolute): "2H 67:00" → 67
    - Extra time (relative): "ET 04:04" → 94, "AET 15:00" → 105
    - Extra time (absolute): "ET 102:53" → 102
    - Stoppage: "45+2:30" → 47, "90+3" → 93
    - Combined: "ET 15:00 +2:43" → 105
    
    Returns:
        Absolute match minute (0-120+), or None if unparseable
    """
    if not raw_clock or raw_clock.upper() in ("NONE", "HT", "FT", "HALF TIME", "FULL TIME"):
        return None
    
    text = raw_clock.upper().strip()
    
    # Detect period indicators (but don't apply offset yet - we need to check if time is relative or absolute)
    has_et = bool(re.search(r'\b(ET|AET|EXTRA\s*TIME)\b', text))
    has_2h = bool(re.search(r'\b(2H|2ND\s*HALF)\b', text))
    has_1h = bool(re.search(r'\b(1H|1ST\s*HALF)\b', text))
    
    # Remove indicators for time parsing
    clean_text = re.sub(r'\b(ET|AET|EXTRA\s*TIME|2H|2ND\s*HALF|1H|1ST\s*HALF)\b', '', text).strip()
    
    # Pattern 1: Stoppage time format "45+2:30" or "90+3" or "45+2"
    # This is ALWAYS explicit - the base is stated directly
    stoppage_match = re.match(r'(\d+)\s*\+\s*(\d+)', clean_text)
    if stoppage_match:
        base_min = int(stoppage_match.group(1))
        added_min = int(stoppage_match.group(2))
        return base_min + added_min
    
    # Pattern 2: Standard MM:SS format (or just MM)
    time_match = re.search(r'(\d{1,3}):(\d{2})', clean_text)
    if not time_match:
        # Try just minutes
        just_minutes = re.match(r'^(\d{1,3})$', clean_text.strip())
        if just_minutes:
            minutes = int(just_minutes.group(1))
        else:
            return None  # Can't parse
    else:
        minutes = int(time_match.group(1))
    
    # SMART OFFSET LOGIC: Determine if time is relative or absolute
    if has_et:
        # Extra time indicator present
        if minutes >= 90:
            # Already absolute (e.g., "ET 102:53")
            return minutes
        elif minutes <= 30:
            # Relative to ET start (e.g., "ET 04:04" or "ET 15:00")
            # ET periods are 15 mins each, so max relative would be ~30
            return 90 + minutes
        else:
            # Ambiguous (31-89) - rare, assume absolute
            # This handles edge cases like "ET 45:00" which would be weird but possible
            return minutes
    
    elif has_2h:
        # Second half indicator present
        if minutes >= 45:
            # Already absolute (e.g., "2H 67:00")
            return minutes
        else:
            # Relative to 2H start (e.g., "2H 5:00")
            return 45 + minutes
    
    elif has_1h:
        # First half indicator - always relative (or already 0-45 absolute)
        # Either way, no offset needed
        return minutes
    
    else:
        # No indicator - assume absolute (most common format)
        # "34:12" → 34, "84:28" → 84, "112:54" → 112
        return minutes
```

### Timestamp validation logic

```python
def validate_timestamp(
    extracted_clocks: list[str | None],  # Raw clock text from 25% and 75% frames
    api_elapsed: int,
    api_extra: int | None
) -> tuple[bool, int | None, str]:
    """
    Check if EITHER extracted clock matches the API-reported event time.
    
    Success criteria: At least ONE of the two frame checks must be within
    the acceptable range. We don't require both.
    
    Args:
        extracted_clocks: Raw clock text from 25% and 75% frames
        api_elapsed: API elapsed minute (e.g. 45)
        api_extra: API extra time (e.g. 2), or None
    
    Returns:
        Tuple of:
        - clock_verified: True if at least one clock matches
        - extracted_minute: The matching minute (or best guess), None if no clock visible
        - timestamp_bucket: "A" (verified), "B" (mismatch), or "C" (no clock)
    """
    # API reports the minute AFTER the goal happened
    # Expected broadcast minute = elapsed + extra - 1
    expected = api_elapsed + (api_extra or 0) - 1
    
    # Parse all clocks
    parsed_minutes = []
    for raw in extracted_clocks:
        parsed = parse_broadcast_clock(raw)
        if parsed is not None:
            parsed_minutes.append(parsed)
    
    if not parsed_minutes:
        # No clock visible in any frame → Bucket C
        return (False, None, "C")
    
    # Check if ANY parsed minute is within ±1 of expected
    for minute in parsed_minutes:
        if abs(minute - expected) <= 1:
            # Match found! → Bucket A
            return (True, minute, "A")
    
    # Clock visible but wrong time → Bucket B (reject)
    # Return the closest one for logging purposes
    closest = min(parsed_minutes, key=lambda m: abs(m - expected))
    return (False, closest, "B")
```

### Test cases for clock parsing

| Raw clock text | Parsed minute | Notes |
|----------------|---------------|-------|
| `"34:12"` | 34 | Standard first half (no indicator) |
| `"84:28"` | 84 | Standard second half (no indicator) |
| `"112:54"` | 112 | Standard extra time (no indicator) |
| `"2H 5:00"` | 50 | 45 + 5 (relative, 5 < 45) |
| `"2H 15:30"` | 60 | 45 + 15 (relative, 15 < 45) |
| `"2H 67:00"` | 67 | Already absolute (67 >= 45, don't add) |
| `"1H 35:00"` | 35 | Explicit first half |
| `"ET 04:04"` | 94 | 90 + 4 (relative, 4 < 30) |
| `"AET 04:04"` | 94 | 90 + 4 (same as ET) |
| `"ET 15:00"` | 105 | 90 + 15 (relative, 15 < 30) |
| `"ET 102:53"` | 102 | Already absolute (102 >= 90, don't add) |
| `"ET 95:00"` | 95 | Already absolute (95 >= 90, don't add) |
| `"ET 15:00 +2:43"` | 105 | 90 + 15 (ignore +seconds) |
| `"45+2:30"` | 47 | First half stoppage (explicit base) |
| `"45+2"` | 47 | Stoppage without seconds |
| `"90+3:15"` | 93 | Second half stoppage (explicit base) |
| `"90+3"` | 93 | Stoppage without seconds |
| `"105+2"` | 107 | ET stoppage (explicit base) |
| `"HT"` | None | Half time, no clock |
| `"FT"` | None | Full time, no clock |
| `None` | None | No clock visible |
| `"NONE"` | None | AI reported no clock |

### What passes, what fails

| API time | Expected | Frame 25% clock | Frame 75% clock | Result |
|----------|----------|-----------------|-----------------|--------|
| 45+2 | 46 | `"45:30"` | `"47:12"` | **PASS** (45 is within ±1 of 46) |
| 95 | 94 | `"ET 04:04"` | `"ET 05:30"` | **PASS** (94 matches, 95 is ±1) |
| 50 | 49 | `"2H 5:00"` | `"2H 6:30"` | **PASS** (50 is within ±1 of 49) |
| 30 | 29 | `"65:00"` | `"72:00"` | **FAIL** (both clocks are wrong half) |
| 30 | 29 | `None` | `"28:45"` | **PASS** (28 is within ±1 of 29, only one needed) |
| 30 | 29 | `None` | `None` | **BUCKET C** (no clock, cannot verify) |
| 30 | 29 | `"HT"` | `"HT"` | **BUCKET C** (HT = no usable clock) |

### When no clock is visible (Bucket C)

If neither frame has a parseable game clock, we **cannot verify** the timestamp. Options:
- **Option A: Pass anyway** — fail open, rely on soccer detection only (current behavior)
- **Option B: Soft fail** — mark as "unverified" but still allow, lower ranking priority
- **Option C: Hard fail** — reject videos with no visible clock

**Current recommendation: Option A (fail open)** — many valid clips crop out the scoreboard (close-up celebrations, replays, fan recordings). We should not reject them, but we use `timestamp_bucket="C"` to keep them separate from verified clips during deduplication.

### Data flow changes needed

1. **`TwitterWorkflowInput`** — add `event_minute: int` and `event_extra: Optional[int]` fields
2. **`DownloadWorkflowInput`** — add `event_minute: int` and `event_extra: Optional[int]` fields  
3. **`validate_video_is_soccer()`** — add `event_minute: int` and `event_extra: Optional[int]` params
4. **`monitor_workflow.py`** — already has `minute` and `extra` in `twitter_triggered`, just needs to pass them through
5. **Vision prompt** — add CLOCK question (report raw text, not parsed minutes)
6. **Parse response** — extract raw CLOCK value as string
7. **Parse clock text** — call `parse_broadcast_clock()` on raw text to get absolute minute
8. **Validate timestamp** — call `validate_timestamp()` with parsed minutes + API time
9. **Return value** — add `clock_verified: bool`, `extracted_minute: int|None`, `timestamp_bucket: str`

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
- `validate_video_is_soccer()` extracts raw clock text from 25% and 75% frames
- Internally calls `parse_broadcast_clock()` to convert "ET 04:04" → 94, "2H 5:00" → 50, etc.
- Calls `validate_timestamp()` to check if EITHER frame matches API time (only one needs to match!)
- Returns `clock_verified: bool`, `extracted_minute: int|None`, `timestamp_bucket: str`
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

## Current Models & Data Flow (Comprehensive Reference)

### Model Definitions (`src/data/models.py`)

#### `APIEventTime` (TypedDict)
Source of truth for when an event happened. Comes from the API's `event.time` object.
```python
class APIEventTime(TypedDict, total=False):
    elapsed: Optional[int]   # Base minute (e.g. 45)
    extra: Optional[int]     # Added time (e.g. 2 for 45+2)
```
**No changes needed** — this is the API input we compare against.

#### `EventFields` (Constants class)
String constants for all underscore-prefixed enhanced fields on events. Prevents typos.
```python
class EventFields:
    EVENT_ID = "_event_id"
    MONITOR_WORKFLOWS = "_monitor_workflows"
    MONITOR_COMPLETE = "_monitor_complete"
    FIRST_SEEN = "_first_seen"
    DOWNLOAD_WORKFLOWS = "_download_workflows"
    DOWNLOAD_COMPLETE = "_download_complete"
    DOWNLOAD_COMPLETED_AT = "_download_completed_at"
    TWITTER_SEARCH = "_twitter_search"
    TWITTER_ALIASES = "_twitter_aliases"
    DROP_WORKFLOWS = "_drop_workflows"
    DISCOVERED_VIDEOS = "_discovered_videos"
    S3_VIDEOS = "_s3_videos"
    VIDEO_COUNT = "_video_count"
    DOWNLOAD_STATS = "_download_stats"
    SCORE_AFTER = "_score_after"
    SCORING_TEAM = "_scoring_team"
    REMOVED = "_removed"
    # DEPRECATED
    MONITOR_COUNT = "_monitor_count"
    TWITTER_COUNT = "_twitter_count"
```
**No changes needed** — event-level fields don't change. Videos within `_s3_videos` get new fields, but those are defined in `VideoFields` / `S3Video`.

#### `VideoFields` (Constants class)
String constants for fields on video objects within `_s3_videos`. Currently only covers 6 of the 13+ actual fields:
```python
class VideoFields:
    URL = "url"
    PERCEPTUAL_HASH = "perceptual_hash"
    RESOLUTION_SCORE = "resolution_score"
    FILE_SIZE = "file_size"
    POPULARITY = "popularity"
    RANK = "rank"
```
**⚠️ Gap:** The `S3Video` TypedDict has `width`, `height`, `aspect_ratio`, `bitrate`, `duration`, `source_url`, `hash_version`, `_s3_key` — none of these have `VideoFields` constants. This is a pre-existing gap, not caused by this refactor.

**Changes needed:**
```python
class VideoFields:
    # ... existing fields ...
    # NEW: Timestamp verification fields
    TIMESTAMP_BUCKET = "timestamp_bucket"
    EXTRACTED_MINUTE = "extracted_minute"
```

#### `S3Video` (TypedDict)
Full schema for video objects stored in MongoDB's `_s3_videos` array. This is the source of truth — S3 metadata may be truncated.
```python
class S3Video(TypedDict, total=False):
    url: str                  # Relative URL: /video/footy-videos/{key}
    _s3_key: str              # S3 key for direct operations
    perceptual_hash: str      # Hash for deduplication
    resolution_score: float
    file_size: int            # File size in bytes
    popularity: int           # Times this clip was found (default: 1)
    rank: int                 # 1=best, higher=worse
    # Quality metadata
    width: int
    height: int
    aspect_ratio: float
    bitrate: int
    duration: float
    source_url: str           # Original tweet URL
    hash_version: str         # Version of hash algorithm used
```
**Changes needed:**
```python
class S3Video(TypedDict, total=False):
    # ... all existing fields ...
    # NEW: Timestamp verification fields
    timestamp_bucket: str     # "A" (verified match) | "C" (no clock visible) — "B" is never stored
    extracted_minute: Optional[int]  # Game clock minute extracted by AI (None if not visible)
```

#### `DownloadStats` (TypedDict)
Pipeline stage counters stored in `_download_stats` on events. Tracks what happened to each video.
```python
class DownloadStats(TypedDict, total=False):
    discovered: int               # Total discovered from Twitter
    downloaded: int               # Successfully downloaded
    filtered_aspect_duration: int # Filtered by aspect/duration
    download_failed: int          # Failed to download
    md5_deduped: int              # Removed by MD5 dedup
    md5_s3_matched: int           # MD5 matched existing S3
    ai_rejected: int              # Not soccer
    ai_validation_failed: int     # AI timeout/error
    hash_generated: int           # Hash generated ok
    hash_failed: int              # Hash failed
    perceptual_deduped: int       # Perceptual dedup removed
    s3_replaced: int              # Replaced lower quality S3
    s3_popularity_bumped: int     # Existing S3 kept, popularity bumped
    uploaded: int                 # Successfully uploaded
```
**Changes needed:**
```python
class DownloadStats(TypedDict, total=False):
    # ... all existing fields ...
    # NEW: Timestamp rejection tracking
    timestamp_rejected: int       # Bucket B — clock visible but wrong minute
```

#### `DiscoveredVideo` (TypedDict)
Raw video metadata from Twitter scraper. Stored in `_discovered_videos`.
```python
class DiscoveredVideo(TypedDict, total=False):
    video_page_url: str
    video_url: str
    tweet_url: str
    tweet_text: str
    username: str
    views: int
    likes: int
    retweets: int
```
**No changes needed** — these are pre-download, timestamp extraction happens during validation.

#### `EnhancedEvent` (TypedDict)
Full event structure with all tracking fields. Contains `_s3_videos: List[S3Video]`.
**No direct changes needed** — inherits S3Video changes automatically.

### Workflow Input Types

#### `TwitterWorkflowInput` (dataclass) — `src/workflows/twitter_workflow.py:65`
```python
@dataclass
class TwitterWorkflowInput:
    fixture_id: int
    event_id: str
    team_id: int                    # API-Football team ID
    team_name: str                  # "Liverpool"
    player_name: Optional[str]      # Can be None
```
**Changes needed:**
```python
@dataclass
class TwitterWorkflowInput:
    fixture_id: int
    event_id: str
    team_id: int
    team_name: str
    player_name: Optional[str]
    # NEW: Event minute for timestamp verification
    event_minute: int = 0                    # API elapsed minute
    event_extra: Optional[int] = None        # API extra time (45+2 → extra=2)
```
Fields are appended with defaults so existing Temporal workflow histories remain compatible.

#### `DownloadWorkflow.run()` — `src/workflows/download_workflow.py:71`
Currently takes positional args (not a dataclass):
```python
async def run(self, fixture_id: int, event_id: str, player_name: str,
              team_name: str, discovered_videos: list) -> dict:
```
**Changes needed:**
```python
async def run(self, fixture_id: int, event_id: str, player_name: str,
              team_name: str, discovered_videos: list,
              event_minute: int = 0, event_extra: int = None) -> dict:
```
Appended with defaults for Temporal replay compatibility.

### Activity Signatures

#### `validate_video_is_soccer()` — `src/activities/download.py:434`
```python
async def validate_video_is_soccer(file_path: str, event_id: str) -> Dict[str, Any]:
```
**Changes needed:**
```python
async def validate_video_is_soccer(
    file_path: str, event_id: str,
    event_minute: int = 0, event_extra: int = None
) -> Dict[str, Any]:
```
Return value currently:
```python
{
    "is_valid": bool,
    "confidence": float,
    "reason": str,
    "is_soccer": bool,
    "is_screen_recording": bool,
    "detected_features": list,
    "checks_performed": int,
}
```
**New return value:**
```python
{
    "is_valid": bool,
    "confidence": float,
    "reason": str,
    "is_soccer": bool,
    "is_screen_recording": bool,
    "detected_features": list,
    "checks_performed": int,
    # NEW
    "clock_verified": bool,            # True if extracted clock matches API time ±1
    "extracted_minute": int | None,     # Best extracted clock minute (None if no clock)
    "timestamp_bucket": str,           # "A", "B", or "C"
}
```

#### `upload_single_video()` — `src/activities/upload.py:700`
```python
async def upload_single_video(
    file_path, fixture_id, event_id, player_name, team_name,
    video_index, file_hash, perceptual_hash, duration, popularity,
    assister_name, opponent_team, source_url,
    width, height, bitrate, file_size, existing_s3_key
) -> Dict[str, Any]:
```
Returns a `video_object` dict that goes into MongoDB:
```python
"video_object": {
    "url", "_s3_key", "perceptual_hash", "resolution_score",
    "file_size", "popularity", "rank",
    "width", "height", "aspect_ratio", "bitrate",
    "duration", "source_url", "hash_version",
}
```
**Changes needed:** Add `timestamp_bucket` and `extracted_minute` params, include in `video_object`:
```python
async def upload_single_video(
    ...,
    existing_s3_key: str = "",
    # NEW
    timestamp_bucket: str = "C",
    extracted_minute: int = None,
) -> Dict[str, Any]:

# In video_object:
"video_object": {
    # ... all existing fields ...
    "timestamp_bucket": timestamp_bucket,
    "extracted_minute": extracted_minute,
}
```

#### `deduplicate_videos()` — `src/activities/upload.py:425`
```python
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None,
    event_id: str = "",
) -> Dict[str, Any]:
```
**Signature stays the same** — bucket info is already on each `downloaded_files` item (added by DownloadWorkflow after validation). The function's internal logic changes to:
1. Separate inputs into buckets A, B, C using `video_info["timestamp_bucket"]`
2. Discard bucket B entirely (log count)
3. Run Phase 1 (batch clustering) independently within A and within C
4. Run Phase 2 (S3 comparison) scoped by bucket — A winners vs A S3 videos, C winners vs C S3 videos
5. Legacy S3 videos (no `timestamp_bucket` field) → treated as bucket C

### Complete Data Flow (End-to-End)

```
API event.time → {elapsed: 45, extra: 2}
        │
        ▼
MonitorWorkflow (monitor_workflow.py:155)
├── process_fixture_events() returns twitter_triggered list
│   └── Each item has: {event_id, player_name, team_id, team_name, minute, extra, first_seen}
│                                                                    ▲▲▲▲▲▲  ▲▲▲▲▲
│                                                              ALREADY EXISTS in monitor
├── Creates TwitterWorkflowInput(
│       fixture_id, event_id, team_id, team_name, player_name,
│       event_minute=minute, event_extra=extra     ◄── NEW: pass through
│   )
└── Starts TwitterWorkflow
        │
        ▼
TwitterWorkflow (twitter_workflow.py:100)
├── Resolves team aliases
├── Searches Twitter, gets discovered_videos
├── Starts DownloadWorkflow(
│       fixture_id, event_id, player_name, team_name, discovered_videos,
│       event_minute=input.event_minute,            ◄── NEW: pass through
│       event_extra=input.event_extra               ◄── NEW: pass through
│   )
        │
        ▼
DownloadWorkflow (download_workflow.py:71)
├── Step 1: Download videos in parallel
├── Step 2: MD5 batch dedup (bucket-agnostic)
├── Step 3: AI Validation
│   └── validate_video_is_soccer(
│           file_path, event_id,
│           event_minute, event_extra               ◄── NEW: pass minute/extra
│       )
│       Returns: {is_valid, clock_verified, extracted_minute, timestamp_bucket, ...}
│                                                    ▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲▲
│                                                    NEW: attach to video_info
│   After validation, for each passing video:
│       video_info["clock_verified"] = validation["clock_verified"]
│       video_info["extracted_minute"] = validation["extracted_minute"]
│       video_info["timestamp_bucket"] = validation["timestamp_bucket"]
│   
│   For bucket B videos: REJECT (don't proceed to hash generation)
│       download_stats["timestamp_rejected"] += 1
│
├── Step 4: Generate perceptual hashes (only bucket A + C videos)
├── Step 5: Signal UploadWorkflow with video_info list
│           (each video_info now has timestamp_bucket + extracted_minute)
        │
        ▼
UploadWorkflow (upload_workflow.py)
├── Step 3: fetch_event_data()
│   └── Loads existing_s3_videos from MongoDB
│       (automatically includes timestamp_bucket + extracted_minute if stored)
├── Step 4: deduplicate_videos(downloaded_files, existing_s3_videos)
│   └── Phase 1: Batch dedup SCOPED BY BUCKET (A vs A, C vs C only)
│   └── Phase 2: S3 dedup SCOPED BY BUCKET
├── Step 6: upload_single_video(
│       ...,
│       timestamp_bucket=video_info["timestamp_bucket"],     ◄── NEW
│       extracted_minute=video_info.get("extracted_minute"),  ◄── NEW
│   )
│   └── Returns video_object with timestamp_bucket + extracted_minute
├── Step 7: save_video_objects() → MongoDB
│   └── video_object now includes timestamp_bucket + extracted_minute
```

### The `video_info` dict lifecycle

The `video_info` dict is a plain dict (not a TypedDict) that accumulates fields as it moves through the DownloadWorkflow pipeline. Here's every field at each stage:

**After download_single_video():**
```python
{
    "status": "success",
    "file_path": "/tmp/found-footy/{event_id}_{run_id}/video_0.mp4",
    "file_hash": "abc123...",       # MD5 hash
    "file_size": 2500000,
    "duration": 45.2,
    "width": 1280,
    "height": 720,
    "bitrate": 3500000,
    "source_url": "https://twitter.com/...",
}
```

**After validate_video_is_soccer() — NEW fields attached:**
```python
{
    # ... all download fields ...
    "clock_verified": True,          # ◄── NEW
    "extracted_minute": 46,          # ◄── NEW (None if no clock)
    "timestamp_bucket": "A",         # ◄── NEW ("A", "B", or "C")
}
```
Bucket B videos are REMOVED from the pipeline at this stage.

**After generate_video_hash():**
```python
{
    # ... all above fields ...
    "perceptual_hash": "dense:0.25:a1b2c3...",
}
```

**After batch MD5 dedup (within DownloadWorkflow):**
```python
{
    # ... all above fields ...
    "popularity": 2,                 # Bumped if MD5 duplicates found
}
```

**Sent to UploadWorkflow via signal — all fields above are preserved.**

**After upload_single_video() → video_object for MongoDB:**
```python
{
    "url": "/video/footy-videos/...",
    "_s3_key": "footy-videos/...",
    "perceptual_hash": "dense:0.25:a1b2c3...",
    "resolution_score": 921600,
    "file_size": 2500000,
    "popularity": 2,
    "rank": 0,
    "width": 1280,
    "height": 720,
    "aspect_ratio": 1.78,
    "bitrate": 3500000,
    "duration": 45.2,
    "source_url": "https://twitter.com/...",
    "hash_version": "dense:0.25",
    "timestamp_bucket": "A",         # ◄── NEW
    "extracted_minute": 46,          # ◄── NEW
}
```

### Call Sites That Need Changes

| Location | Current call | Change needed |
|----------|-------------|---------------|
| `monitor_workflow.py:201` | `TwitterWorkflowInput(fixture_id, event_id, team_id, team_name, player_name)` | Add `event_minute=minute, event_extra=extra` |
| `twitter_workflow.py:485` | `args=[input.fixture_id, input.event_id, input.player_name, team_aliases[0], videos_to_download]` | Append `input.event_minute, input.event_extra` |
| `download_workflow.py:300` | `args=[video_info["file_path"], event_id]` | Append `event_minute, event_extra` |
| `download_workflow.py:309` | Checks `validation.get("is_valid")` only | Also check `timestamp_bucket == "B"` → reject |
| `download_workflow.py:315` | Appends to `validated_videos` | Attach `clock_verified`, `extracted_minute`, `timestamp_bucket` to `video_info` |
| `upload_workflow.py:438` | `args=[..., existing_s3_key]` | Append `timestamp_bucket, extracted_minute` |

### Backward Compatibility

1. **Temporal replay safety:** All new params use defaults (`event_minute=0`, `event_extra=None`, `timestamp_bucket="C"`, `extracted_minute=None`). In-flight workflows replay cleanly — they get bucket C (no clock info), matching current behavior.

2. **MongoDB migration:** None needed. Existing `_s3_videos` documents without `timestamp_bucket` are treated as bucket C. The `total=False` on `S3Video` TypedDict means all fields are optional. `fetch_event_data()` loads full video objects, so new fields are automatically available when present.

3. **DownloadStats:** Existing stats objects without `timestamp_rejected` are valid — `TypedDict(total=False)` makes it optional.

---

## Implementation Order

1. ✅ Create branch `refactor/valid-deduplication`
2. ⬜ Add `event_minute` + `event_extra` to workflow inputs (`TwitterWorkflowInput`, `DownloadWorkflow.run()`)
3. ⬜ Update vision prompt to extract game clock (add CLOCK question)
4. ⬜ Add CLOCK parsing to `parse_response()` inner function
5. ⬜ Add timestamp validation logic + bucket assignment in `validate_video_is_soccer()`
6. ⬜ Wire `event_minute`/`event_extra` through monitor → twitter → download → validate call sites
7. ⬜ Attach `clock_verified`, `extracted_minute`, `timestamp_bucket` to `video_info` in DownloadWorkflow
8. ⬜ Add bucket B rejection in DownloadWorkflow (between validation and hash generation)
9. ⬜ Update `deduplicate_videos()` for bucket-scoped dedup (batch + S3 phases)
10. ⬜ Add `timestamp_bucket` + `extracted_minute` params to `upload_single_video()`, include in `video_object`
11. ⬜ Pass new fields through `upload_workflow.py` call site
12. ⬜ Add `timestamp_rejected` to `download_stats` initialization in DownloadWorkflow
13. ⬜ Test with live data
14. ⬜ Deploy and monitor clock extraction accuracy + bucket distribution

---

## Files to modify

| File | Changes |
|------|---------|
| `src/data/models.py` | Add `TIMESTAMP_BUCKET` + `EXTRACTED_MINUTE` to `VideoFields`, add fields to `S3Video` TypedDict, add `timestamp_rejected` to `DownloadStats` |
| `src/workflows/twitter_workflow.py` | Add `event_minute`, `event_extra` to `TwitterWorkflowInput` dataclass; pass to `DownloadWorkflow.run()` args |
| `src/workflows/download_workflow.py` | Accept `event_minute`/`event_extra` in `run()`; pass to `validate_video_is_soccer()`; attach bucket fields to `video_info`; reject bucket B; add `timestamp_rejected` to `download_stats` |
| `src/workflows/monitor_workflow.py` | Pass `event_minute=minute, event_extra=extra` to `TwitterWorkflowInput` |
| `src/activities/download.py` | Update prompt with CLOCK question; update `parse_response()` to return 3-tuple; add timestamp validation + bucket assignment; update `validate_video_is_soccer()` signature + return value |
| `src/activities/upload.py` | Bucket-scoped dedup in `deduplicate_videos()` (both phases); add `timestamp_bucket`/`extracted_minute` params to `upload_single_video()`; include in `video_object` |
| `src/workflows/upload_workflow.py` | Pass `timestamp_bucket` + `extracted_minute` from `video_info` to `upload_single_video()` args |
