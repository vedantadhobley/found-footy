# Found Footy - Temporal Workflow Architecture

## Overview

This system uses Temporal.io to orchestrate the discovery, tracking, and archival of football goal videos from social media. The architecture consists of **5 workflows** that form a parent-child cascade, managing the full pipeline from fixture ingestion to video download.

**Key Features:**
- **Decoupled architecture** - TwitterWorkflow manages its own retries, not Monitor
- **Durable timers** - 3-minute spacing between Twitter attempts survives restarts
- **Per-activity retry with exponential backoff** - Granular failure recovery
- **3 Twitter attempts per event** - Better video quality over time
- **Multi-alias search** - Search "Salah Liverpool", "Salah LFC", "Salah Reds"
- **Cross-retry quality replacement** - Higher resolution videos replace lower ones

---

## Workflow Hierarchy

```
┌─────────────────────────────────────────────────────────────────────┐
│                      SCHEDULED WORKFLOWS                             │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│   IngestWorkflow                    MonitorWorkflow                  │
│   (Daily 00:05 UTC)                 (Every 1 Minute)                │
│        │                                  │                          │
│   Fetch fixtures                     Poll API                        │
│   Route by status                    Debounce events                 │
│                                      Trigger RAG when stable         │
│                                           │                          │
└───────────────────────────────────────────┼─────────────────────────┘
                                            │ (fire-and-forget)
                                            ▼
┌─────────────────────────────────────────────────────────────────────┐
│                         RAGWorkflow                                  │
│   - get_team_aliases(team) → ["Liverpool", "LFC", "Reds"]           ││   - Uses Wikidata + llama.cpp LLM for alias selection                ││   - save_team_aliases to MongoDB                                     │
│   - Start TwitterWorkflow (child, waits)                             │
│                              │                                       │
└──────────────────────────────┼──────────────────────────────────────┘
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│                 TwitterWorkflow (Self-Managing)                      │
│                                                                      │
│   FOR attempt IN [1, 2, 3]:                                         │
│     - update_twitter_attempt(attempt)                                │
│     - Search each alias with player name                             │
│     - Dedupe videos (by URL)                                         │
│     - save_discovered_videos                                         │
│     - Start DownloadWorkflow (child, waits)                          │
│     - IF attempt < 3: workflow.sleep(3 min) ← DURABLE TIMER          │
│                              │                                       │
│   AFTER attempt 3: mark_event_twitter_complete                       │
│                              │                                       │
└──────────────────────────────┼──────────────────────────────────────┘
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│                      DownloadWorkflow                                │
│   - Download videos via yt-dlp                                       │
│   - Filter by duration (5-60s)                                       │
│   - Compute perceptual hash                                          │
│   - Compare quality with existing S3                                 │
│   - Upload new/better videos                                         │
│   - Replace worse quality versions                                   │
└─────────────────────────────────────────────────────────────────────┘
```

**Key Architecture Points:**
- `start_child_workflow` with `parent_close_policy=ABANDON` for Monitor → RAG
- `execute_child_workflow` for RAG → Twitter → Download (waits for completion)
- Durable timers (`workflow.sleep`) in TwitterWorkflow for 3-min spacing

---

## Workflow Naming Convention

All workflows use human-readable IDs for easy debugging in Temporal UI:

| Workflow | ID Format | Example |
|----------|-----------|---------|
| **IngestWorkflow** | `ingest-{DD_MM_YYYY}` | `ingest-05_12_2024` |
| **MonitorWorkflow** | `monitor-{DD_MM_YYYY}-{HH:MM}` | `monitor-05_12_2024-15:23` |
| **RAGWorkflow** | `rag-{Team}-{LastName}-{min}-{event_id}` | `rag-Liverpool-Salah-45+3min-123456_40_306_Goal_1` |
| **TwitterWorkflow** | `twitter-{Team}-{LastName}-{min}-{event_id}` | `twitter-Liverpool-Salah-45+3min-123456_40_306_Goal_1` |
| **DownloadWorkflow** | `download{N}-{Team}-{LastName}-{count}vids-{event_id}` | `download1-Liverpool-Salah-3vids-123456_40_306_Goal_1` |

---

## 1. IngestWorkflow

**Schedule**: Daily at 00:05 UTC  
**Purpose**: Fetch today's fixtures and route to correct collections

```
IngestWorkflow
    │
    ├── fetch_todays_fixtures
    │   └── GET /fixtures?date=today from API-Football
    │
    └── categorize_and_store_fixtures
        ├── TBD, NS → fixtures_staging
        ├── LIVE, 1H, HT, 2H → fixtures_active
        └── FT, AET, PEN → fixtures_completed
```

### Activities

| Activity | Timeout | Retries | Backoff |
|----------|---------|---------|---------|
| `fetch_todays_fixtures` | 30s | 3 | 2.0x from 1s |
| `categorize_and_store_fixtures` | 30s | 3 | 2.0x from 1s |

---

## 2. MonitorWorkflow

**Schedule**: Every minute  
**Purpose**: Poll active fixtures, debounce events, trigger RAG for stable events

```
MonitorWorkflow (every 1 min)
    │
    ├── fetch_staging_fixtures → process_staging_fixtures
    │   └── Update staging fixtures with fresh API data
    │
    ├── activate_pending_fixtures
    │   └── Move staging → active when start time reached
    │
    ├── fetch_active_fixtures
    │   └── Batch GET from API-Football
    │
    ├── FOR each fixture:
    │   ├── store_and_compare
    │   │   └── Filter to Goals, store in fixtures_live
    │   │
    │   ├── process_fixture_events
    │   │   ├── NEW events: Add with _monitor_count=1
    │   │   ├── EXISTING events: Increment _monitor_count
    │   │   ├── REMOVED events: Mark _removed=true
    │   │   └── IF _monitor_count >= 3: Add to twitter_triggered
    │   │
    │   ├── FOR each twitter_triggered:
    │   │   └── start_child_workflow(RAGWorkflow) ← FIRE-AND-FORGET
    │   │
    │   └── IF fixture FT/AET/PEN:
    │       └── complete_fixture_if_ready
    │
    └── notify_frontend_refresh
```

### Activities

| Activity | Timeout | Retries | Backoff |
|----------|---------|---------|---------|
| `fetch_staging_fixtures` | 60s | 3 | - |
| `process_staging_fixtures` | 30s | 3 | - |
| `activate_pending_fixtures` | 30s | 2 | - |
| `fetch_active_fixtures` | 60s | 3 | - |
| `store_and_compare` | 10s | 3 | 2.0x from 1s |
| `process_fixture_events` | 60s | 3 | - |
| `complete_fixture_if_ready` | 10s | 3 | 2.0x from 1s |
| `notify_frontend_refresh` | 5s | 1 | - |

### Event Processing Logic

```python
# Pure set operations - no hash comparison needed!
live_ids = {e["_event_id"] for e in live_events}
active_ids = {e["_event_id"] for e in active_events}

new_ids = live_ids - active_ids       # NEW events → add with count=1
removed_ids = active_ids - live_ids   # VAR disallowed → mark _removed
matching_ids = live_ids & active_ids  # Existing → check count

for event_id in matching_ids:
    if event._monitor_complete:
        continue  # Already triggered, skip
    
    event._monitor_count += 1
    
    if event._monitor_count >= 3:
        event._monitor_complete = True
        twitter_triggered.append(event)  # Will trigger RAGWorkflow
```

---

## 3. RAGWorkflow

**Trigger**: Fire-and-forget from MonitorWorkflow when `_monitor_complete=true`  
**Purpose**: Resolve team aliases via Wikidata RAG + LLM, then trigger TwitterWorkflow

```
RAGWorkflow
    │
    ├── get_cached_team_aliases(team_id)
    │   └── Fast O(1) lookup in team_aliases collection
    │   └── Pre-cached during ingestion for both teams
    │
    ├── IF cache miss: get_team_aliases(team_id, team_name)
    │   ├── Call API-Football /teams?id={id} → get team data + venue
    │   │   └── team.national, team.country, venue.city
    │   ├── Query Wikidata for team QID (uses country + city for disambiguation)
    │   ├── Fetch Wikidata aliases
    │   ├── Preprocess to single words (filter junk)
    │   ├── llama.cpp LLM selects best words for Twitter
    │   └── Cache with national, country, city, and timestamps
    │
    ├── save_team_aliases
    │   └── Store to _twitter_aliases in event
    │
    └── execute_child_workflow(TwitterWorkflow)
        └── Waits for completion (3 attempts)
```

### Activities

| Activity | Timeout | Retries | Purpose |
|----------|---------|---------|---------|
| `get_cached_team_aliases` | 10s | 2 | Fast MongoDB cache lookup |
| `get_team_aliases` | 60s | 2 | Full RAG pipeline (Wikidata + LLM) |
| `save_team_aliases` | 10s | 2 | Store to event in MongoDB |

### Alias Examples (Real Output)

```
"Atletico de Madrid" → ["ATM", "Atletico", "Atleti", "Madrid"]
"Manchester United"  → ["MUFC", "Utd", "Devils", "Manchester", "United"]
"Liverpool"          → ["LFC", "Reds", "Anfield", "Liverpool"]
"Belgium"            → ["Belgian", "Belgique", "Belgium"]  # National team
"Mali"               → ["Malian", "Mali"]  # National team (ID 1500)
```

### Team Type Detection

Team type (club vs national) is determined by API-Football, NOT by team ID:
- API returns `team.national: true` for national teams
- Stored in `team_aliases` collection as `national` boolean
- National teams get nationality adjectives added ("Belgian", "French")

---

## 4. TwitterWorkflow

**Trigger**: Child workflow from RAGWorkflow  
**Purpose**: Search Twitter for videos, manage 3 attempts with durable timers

```
TwitterWorkflow (self-managing)
    │
    FOR attempt IN [1, 2, 3]:
    │   │
    │   ├── update_twitter_attempt(attempt)
    │   │   └── Set _twitter_count in MongoDB
    │   │
    │   ├── get_twitter_search_data
    │   │   └── Get existing video URLs for dedup
    │   │
    │   ├── FOR each alias in team_aliases:
    │   │   ├── execute_twitter_search("{player_last} {alias}")
    │   │   │   └── POST to Firefox service with exclude_urls
    │   │   └── Collect videos, dedupe by URL
    │   │
    │   ├── save_discovered_videos
    │   │   └── Append to _discovered_videos in MongoDB
    │   │
    │   ├── IF videos found:
    │   │   └── execute_child_workflow(DownloadWorkflow)
    │   │
    │   └── IF attempt < 3:
    │       └── workflow.sleep(3 minutes) ← DURABLE TIMER
    │
    └── mark_event_twitter_complete
        └── Set _twitter_complete=true in MongoDB
```

### Activities

| Activity | Timeout | Retries | Backoff |
|----------|---------|---------|---------|
| `update_twitter_attempt` | 10s | 2 | - |
| `get_twitter_search_data` | 10s | 2 | - |
| `execute_twitter_search` | 150s | 3 | 1.5x from 10s |
| `save_discovered_videos` | 10s | 3 | 2.0x from 1s |
| `mark_event_twitter_complete` | 10s | 3 | - |

### 3-Minute Timer Logic (START-to-START)

```python
# Record attempt start time
attempt_start = workflow.now()

# ... execute search, download, etc. ...

# Wait remainder of 3 minutes from START of this attempt
if attempt < 3:
    elapsed = (workflow.now() - attempt_start).total_seconds()
    wait_seconds = max(180 - elapsed, 30)  # 3 min minus elapsed, min 30s
    await workflow.sleep(timedelta(seconds=wait_seconds))
```

This ensures exactly 3 minutes from the START of one attempt to the START of the next,
regardless of how long the search/download takes.

---

## 5. DownloadWorkflow

**Trigger**: Child workflow from TwitterWorkflow (per attempt with videos)  
**Purpose**: Download, filter, deduplicate, upload videos to S3

```
DownloadWorkflow
    │
    ├── fetch_event_data
    │   └── Get existing _s3_videos metadata
    │
    ├── FOR each video URL:
    │   │
    │   ├── download_single_video
    │   │   ├── yt-dlp download to /tmp
    │   │   ├── ffprobe for duration/metadata
    │   │   └── IF duration < 5s OR > 60s: FILTER (skip)
    │   │
    │   └── deduplicate_videos
    │       ├── Compute perceptual hash
    │       └── Compare with existing S3 videos
    │
    ├── FOR each duplicate with better quality:
    │   └── replace_s3_video (delete old)
    │
    ├── FOR each new/better video:
    │   └── upload_single_video (to S3)
    │
    └── mark_download_complete
        └── Save _s3_videos, cleanup temp dir
```

### Activities

| Activity | Timeout | Retries | Backoff |
|----------|---------|---------|---------|
| `fetch_event_data` | 15s | 2 | - |
| `download_single_video` | 60s | 3 | 2.0x from 2s |
| `deduplicate_videos` | 30s | 2 | - |
| `replace_s3_video` | 15s | 3 | 2.0x from 2s |
| `upload_single_video` | 30s | 3 | 1.5x from 2s |
| `mark_download_complete` | 10s | 3 | 2.0x from 1s |

### Perceptual Hash (Dense Sampling)

```python
def compute_perceptual_hash(file_path, duration):
    """
    Dense sampling perceptual hash with histogram equalization.
    Handles videos with different start times (offsets).
    
    Algorithm:
    1. Sample frames every 0.25 seconds
    2. Apply histogram equalization (normalize contrast)
    3. Resize to 9x8 grayscale
    4. Compute dHash (64-bit difference hash) per frame
    
    Format: "dense:0.25:<ts1>=<hash1>,<ts2>=<hash2>,..."
    """
    interval = 0.25
    hashes = []
    t = interval
    while t < duration - 0.3:
        frame = extract_frame(file_path, t)
        img = ImageOps.equalize(frame.convert('L'))  # Histogram equalization
        img = img.resize((9, 8), Image.Resampling.LANCZOS)
        
        # Compute dHash: compare adjacent pixels
        pixels = list(img.getdata())
        hash_bits = []
        for row in range(8):
            for col in range(8):
                hash_bits.append('1' if pixels[row*9+col] < pixels[row*9+col+1] else '0')
        hash_hex = format(int(''.join(hash_bits), 2), '016x')
        hashes.append(f"{t:.2f}={hash_hex}")
        t += interval
    
    return f"dense:{interval}:{','.join(hashes)}"
```

### Matching Algorithm (3 Consecutive Frames)

**Problem**: Single-frame matching causes false positives between similar content (e.g., two goals in same match).

**Solution**: Require 3 consecutive frames to match at a consistent time offset.

```python
def hashes_match(hash_a, hash_b, max_hamming=10, min_consecutive=3):
    """
    Check if two dense hashes represent the same video.
    
    Algorithm:
    1. Try all possible time offsets between videos
    2. For each offset, count consecutive matching frames
    3. Return True if any offset has >= 3 consecutive matches
    
    A frame matches if hamming distance <= 10 bits (of 64).
    """
```

**Why 3 consecutive?** Random similarity might match 1-2 frames, but 3 consecutive frames at the same offset strongly indicates the same video content.

### Popularity Scoring

**Purpose**: Track how many times the same video content is discovered (higher = more trusted)

**Two-Phase Deduplication** (batch-first for efficiency):

```
PHASE 1: Batch Dedup
├── Compare videos within current download batch
├── Keep highest quality (largest file_size)
├── Sum popularities: winner.pop += loser.pop
└── Delete lower quality local files

PHASE 2: S3 Dedup
├── Compare batch winners against existing S3 videos
├── IF batch > S3 quality: REPLACE (upload batch, delete S3)
│   └── New popularity = batch.pop + s3.pop
├── IF S3 > batch quality: SKIP (keep S3)
│   └── Bump S3 popularity += batch.pop
└── IF no S3 match: UPLOAD (new video)
```

**Why batch-first?** If 3 copies of same video come from different alias searches (Liverpool, LFC, Reds), we reduce to 1 winner BEFORE hitting S3. This means 1 S3 operation instead of 3.

---

## Event Enhancement Fields

| Field | Type | Set By | When |
|-------|------|--------|------|
| `_event_id` | string | Monitor | When first seen |
| `_monitor_count` | int | Monitor | Each poll |
| `_monitor_complete` | bool | Monitor | When `_monitor_count >= 3` |
| `_twitter_aliases` | array | RAGWorkflow | After alias lookup |
| `_twitter_count` | int | TwitterWorkflow | Start of each attempt |
| `_twitter_complete` | bool | TwitterWorkflow | After attempt 3 |
| `_first_seen` | datetime | Monitor | When first detected |
| `_removed` | bool | Monitor | When VAR disallows |
| `_discovered_videos` | array | TwitterWorkflow | After each search |
| `_s3_videos` | array | DownloadWorkflow | After upload |

---

## Timeline Example

```
T+0:00  Goal scored! Event appears in API

T+1:00  Monitor poll #1
        → NEW event detected
        → _monitor_count = 1

T+2:00  Monitor poll #2
        → Event still present
        → _monitor_count = 2

T+3:00  Monitor poll #3
        → _monitor_count = 3
        → _monitor_complete = TRUE
        → RAGWorkflow started (fire-and-forget)

T+3:05  RAGWorkflow
        → get_team_aliases("Liverpool") → ["Liverpool"]
        → TwitterWorkflow started

T+3:10  TwitterWorkflow Attempt 1
        → _twitter_count = 1
        → Search "Salah Liverpool" → 4 videos
        → DownloadWorkflow → 3 uploaded
        → workflow.sleep(~3 min)

T+6:00  TwitterWorkflow Attempt 2
        → _twitter_count = 2
        → Search (with exclude_urls) → 1 new video
        → DownloadWorkflow → 1 uploaded
        → workflow.sleep(~3 min)

T+9:00  TwitterWorkflow Attempt 3
        → _twitter_count = 3
        → Search → 0 new videos
        → _twitter_complete = TRUE

T+10:00 Monitor poll #1 after Twitter complete
        → Fixture status = FT
        → All events: _monitor_complete = TRUE, _twitter_complete = TRUE
        → complete_fixture_if_ready: _completion_count = 1

T+11:00 Monitor poll #2
        → complete_fixture_if_ready: _completion_count = 2

T+12:00 Monitor poll #3
        → complete_fixture_if_ready: _completion_count = 3
        → Fixture moved to fixtures_completed
```

### Completion Counter Logic

The completion counter **only starts** after ALL events are fully processed:

```
complete_fixture_if_ready flow:
    │
    ├── 1. Check ALL events have _monitor_complete = TRUE
    │   └── If not: return False (don't increment counter)
    │
    ├── 2. Check ALL events have _twitter_complete = TRUE
    │   └── If not: return False (don't increment counter)
    │
    ├── 3. ONLY NOW: increment _completion_count (1 → 2 → 3)
    │   └── Logs "COMPLETION STARTED" on first increment
    │
    └── 4. When count >= 3 (or winner data exists):
        └── Move fixture to fixtures_completed
```

This ensures the 3-minute completion debounce doesn't start ticking while
Twitter workflows are still running. The counter only measures "stability
after all processing is done".

---

## Retry Strategy Summary

| Activity Type | Max Attempts | Initial Interval | Backoff |
|--------------|--------------|------------------|---------|
| MongoDB reads | 2-3 | 1s | 2.0x |
| MongoDB writes | 3 | 1s | 2.0x |
| API-Football | 3 | 1s | 2.0x |
| Twitter search | 3 | 10s | 1.5x |
| Video download | 3 | 2s | 2.0x |
| S3 upload | 3 | 2s | 1.5x |

---

## Error Handling

### TwitterWorkflow fails mid-attempt
- Temporal retries the workflow from last checkpoint
- `_twitter_count` shows which attempt was in progress
- Previously downloaded videos are preserved in S3

### Worker crashes during sleep
- Durable timer (`workflow.sleep`) survives restarts
- Workflow resumes after timer expires
- No lost state

### LLM unavailable (future)
- RAGWorkflow activity falls back to `[team_name]`
- Search still works with single alias

### Event removed (VAR)
- Event marked `_removed = TRUE`
- Ignored in completion checks
- Running workflows continue but results are orphaned
