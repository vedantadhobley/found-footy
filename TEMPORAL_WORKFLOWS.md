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
│   1. Download videos via yt-dlp                                      │
│   2. Filter by duration (>3s to 60s)                                 │
│   3. AI validation (reject non-football)                             │
│   4. Compute perceptual hash (validated only)                        │
│   5. Compare quality with existing S3                                │
│   6. Upload new/better videos                                        │
│   7. Replace worse quality versions                                  │
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

## Manual Operations from Temporal UI

You can manually trigger workflows from the Temporal UI at `http://localhost:3100`.

### Manual Fixture Ingest

To manually ingest specific fixtures (bypasses team filter):

1. Go to Temporal UI → **Start Workflow**
2. Fill in:
   - **Workflow Type**: `IngestWorkflow`
   - **Workflow ID**: `manual-ingest-{timestamp}` (e.g., `manual-ingest-20251230`)
   - **Task Queue**: `found-footy`
   - **Input**:
     ```json
     {"fixture_ids": [1379142, 1347263, 1396377]}
     ```

### Input Options

| Input | Description | Example |
|-------|-------------|---------|
| `{}` | Standard daily ingest (today's fixtures for tracked teams) | `{}` |
| `{"target_date": "2025-12-26"}` | Ingest for a specific date (tracked teams only) | `{"target_date": "2025-12-26"}` |
| `{"fixture_ids": [1234, 5678]}` | Ingest specific fixtures by ID (any team/league) | `{"fixture_ids": [1379142]}` |

> **Note**: When using `fixture_ids`, the team filter is bypassed - you can ingest fixtures from any team or league.

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
**Duration**: ~30-90 seconds (quick alias lookup)  
**Purpose**: Resolve team aliases via Wikidata RAG + LLM, then trigger TwitterWorkflow

```
RAGWorkflow (~30-90s)
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
    └── start_child_workflow(TwitterWorkflow, ABANDON)
        └── FIRE-AND-FORGET: RAG completes immediately!
        └── Twitter runs independently (~12-15 min)
```

**Why Fire-and-Forget?**
- RAG's job is quick alias lookup (~30-90s)
- No reason to keep RAG workflow open for 15+ minutes
- Twitter manages its own lifecycle independently
- Reduces workflow state in Temporal server

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

**Trigger**: Fire-and-forget from RAGWorkflow (runs independently)  
**Duration**: ~12-15 minutes (3 attempts + 2 waits of 3 min + downloads)  
**Purpose**: Search Twitter for videos, manage 3 attempts with durable timers

```
TwitterWorkflow (~12-15 min, runs independently)
    │
    FOR attempt IN [1, 2, 3]:
    │   │
    │   ├── check_event_exists (graceful termination for VAR)
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
    │   │   └── execute_child_workflow(DownloadWorkflow) ← MUST WAIT!
    │   │
    │   └── IF attempt < 3:
    │       └── workflow.sleep(3 minutes - elapsed) ← DURABLE TIMER
    │
    └── mark_event_twitter_complete
        └── Set _twitter_complete=true ONLY after all downloads done
```

**Why Twitter WAITS for Download (Critical for Data Integrity):**
- Download updates MongoDB with S3 video URLs
- We mark `_twitter_complete=true` only after all downloads finish
- Fixture completion checks `_twitter_complete` flag
- If fire-and-forget, fixture could move to completed before downloads finish
- This would cause lookups in wrong collection → **data loss**

### Activities

| Activity | Timeout | Retries | Notes |
|----------|---------|---------|-------|
| `check_event_exists` | 30s | 3 | Graceful termination check |
| `update_twitter_attempt` | 30s | 3 | Update counter in MongoDB |
| `get_twitter_search_data` | 30s | 3 | Get existing URLs for dedup |
| `execute_twitter_search` | 180s | 3 | **Heartbeat every 15s** during browser automation |
| `save_discovered_videos` | 30s | 3 | 2.0x from 2s |
| `mark_event_twitter_complete` | 30s | 5 | Critical - more retries |

### Twitter Search Heartbeat Pattern

The `execute_twitter_search` activity sends heartbeats every 15 seconds during browser automation:

```python
# Background asyncio task sends heartbeats
async def heartbeat_loop():
    count = 0
    while not search_complete:
        await asyncio.sleep(15)
        if not search_complete:
            count += 1
            activity.heartbeat(f"Searching... ({count * 15}s elapsed)")

# Activity runs browser automation in parallel with heartbeat loop
asyncio.create_task(heartbeat_loop())
# ... browser operations ...
search_complete = True  # Stops heartbeat loop
```

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
**Purpose**: Download, validate, filter, deduplicate, upload videos to S3

```
DownloadWorkflow (parallel processing, heartbeat-based timeouts)
    │
    ├── fetch_event_data
    │   └── Get existing _s3_videos from MongoDB (source of truth)
    │
    ├── PARALLEL: download_single_video × N
    │   ├── yt-dlp download to /tmp/{event_id}_{run_id}/
    │   ├── ffprobe for duration/metadata
    │   └── IF duration <= 3s OR > 60s: FILTER (skip)
    │
    ├── SEQUENTIAL: validate_video_is_soccer × N (fail-closed)
    │   └── IF not football content: REJECT (skip)
    │
    ├── PARALLEL: generate_video_hash × N (validated only)
    │   └── Heartbeat every 10 frames (~2.5s of video)
    │   └── heartbeat_timeout=30s (kills only truly hung activities)
    │
    ├── deduplicate_videos
    │   └── Compare with existing videos (MongoDB data)
    │
    ├── PARALLEL: replace_s3_video × N (if better quality)
    │
    ├── PARALLEL: upload_single_video × N
    │
    └── mark_download_complete
        └── Save _s3_videos, cleanup temp dir, notify frontend
```

**Key Design Points:**
- **Parallel processing**: Downloads, hashes, uploads run concurrently
- **Heartbeat-based timeouts**: Hash generation uses `heartbeat_timeout=30s` instead of fixed `start_to_close_timeout`
- **Unique temp dirs**: Each workflow run gets `/tmp/footy_{event_id}_{run_id}/` to prevent conflicts
- **AI before hash**: Validation runs BEFORE expensive hash generation

### Activities

| Activity | Timeout | Retries | Notes |
|----------|---------|---------|-------|
| `fetch_event_data` | 30s | 3 | MongoDB read |
| `download_single_video` | 90s | 3 | 2.0x backoff from 2s |
| `validate_video_is_soccer` | 90s | 4 | **Heartbeat between each AI call** |
| `generate_video_hash` | **heartbeat: 30s** | 2 | Heartbeat every 10 frames |
| `deduplicate_videos` | 60s | 3 | Hash comparison |
| `replace_s3_video` | 30s | 3 | S3 delete |
| `upload_single_video` | 30s | 3 | S3 upload |
| `mark_download_complete` | 30s | 3 | MongoDB + cleanup |

### Heartbeat-Based Timeout Strategy

For long-running activities, we use **heartbeat_timeout** instead of arbitrary **execution_timeout**.
Heartbeats prove the activity is making progress - if no heartbeat for 30s, the activity is truly hung.

**Activities using heartbeats:**

1. **`execute_twitter_search`**: Heartbeat every 15s during browser automation
2. **`validate_video_is_soccer`**: Heartbeat between each AI vision call (25%, 75%, 50% tiebreaker)
3. **`generate_video_hash`**: Heartbeat every 10 frames (~2.5s of video)

### Hash Generation Heartbeat

```python
# Activity sends heartbeat every 10 frames (~2.5s of video)
hash_result = await workflow.execute_activity(
    download_activities.generate_video_hash,
    args=[file_path, duration],
    heartbeat_timeout=timedelta(seconds=30),  # Kill if no progress for 30s
    start_to_close_timeout=timedelta(seconds=300),  # Safety net
)

# Inside the activity:
def _generate_perceptual_hash(file_path, duration, heartbeat_fn=None):
    frame_count = 0
    while t < duration:
        # ... process frame ...
        frame_count += 1
        if heartbeat_fn and frame_count % 10 == 0:
            heartbeat_fn()  # Signal "I'm still alive"
```

### AI Vision Heartbeat

```python
# Heartbeat between each AI vision call
activity.heartbeat("AI vision check 1/2 (25% frame)...")
response_25 = await _call_vision_model(frame_25, prompt)

activity.heartbeat("AI vision check 2/2 (75% frame)...")
response_75 = await _call_vision_model(frame_75, prompt)

# Tiebreaker (if needed)
activity.heartbeat("AI vision tiebreaker (50% frame)...")
response_50 = await _call_vision_model(frame_50, prompt)
```

**Benefits:**
- Activities aren't killed while waiting in queue (`schedule_to_start_timeout` removed)
- Long videos (~45s, ~180 frames) complete successfully
- Truly hung activities are killed after 30s of no progress

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

---

## Logging Conventions

All activities use **structured logging** with consistent prefixes for easy filtering and debugging.

### Activity Log Prefixes

| Prefix | Activity | Example |
|--------|----------|---------|
| `[MONITOR]` | Monitor activities | `[MONITOR] Activated fixture \| id=123 \| match=Liverpool vs Arsenal` |
| `[TWITTER]` | Twitter search activities | `[TWITTER] Search complete \| query='Salah Liverpool' \| found=4 videos` |
| `[DOWNLOAD]` | Video download | `[DOWNLOAD] Starting download \| event=123_45_Goal_1 \| video_idx=0` |
| `[VALIDATE]` | AI vision validation | `[VALIDATE] PASSED validation \| event=123_45_Goal_1 \| checks=2` |
| `[HASH]` | Perceptual hash | `[HASH] Generated hash \| frames=120 \| interval=0.25s` |
| `[DEDUP]` | Deduplication | `[DEDUP] Clustered videos \| clusters=3 \| from_downloads=5` |
| `[UPLOAD]` | S3 upload | `[UPLOAD] Uploaded to S3 \| event=123_45_Goal_1 \| url=/video/footy/...` |
| `[RAG]` | RAG workflow | `[RAG] Processing event \| team=Liverpool \| player=Salah` |

### Log Levels

| Emoji | Level | Meaning |
|-------|-------|---------|
| ✅ | INFO | Successful operation |
| ⚠️ | WARNING | Non-critical issue, continuing |
| ❌ | ERROR | Critical failure, may retry |
| 📥 | INFO | Starting operation |
| 🔍 | INFO | Search/lookup operation |
| 💾 | INFO | Database write |
| ☁️ | INFO | Cloud operation (S3) |
| 🏁 | INFO | Completion marker |

### Example Log Output

```
📥 [DOWNLOAD] fetch_event_data | fixture=123456 | event=123456_45_306_Goal_1
✅ [DOWNLOAD] Found videos | event=123456_45_306_Goal_1 | count=3
📥 [DOWNLOAD] Starting download | event=123456_45_306_Goal_1 | video_idx=0
🔍 [VALIDATE] AI vision check 1/2 | SOCCER=YES | SCREEN=NO
✅ [VALIDATE] PASSED validation | event=123456_45_306_Goal_1 | checks=2
🔐 [HASH] Starting hash generation | file=video.mp4 | duration=15.2s
✅ [HASH] Generated hash | frames=60 | interval=0.25s
☁️ [UPLOAD] Starting S3 upload | event=123456_45_306_Goal_1 | quality=1920x1080
✅ [UPLOAD] Uploaded to S3 | event=123456_45_306_Goal_1 | url=/video/footy-videos/...
```
