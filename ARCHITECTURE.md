# Found Footy - Architecture Guide

**Temporal.io orchestration with 4-collection MongoDB architecture**

## 🎯 Core Concept

**4-Collection Design with fixtures_live for Safe Comparison**

Raw API data is stored in `fixtures_live` (temporary, overwritten each poll) for comparison, while `fixtures_active` contains enhanced events that are **never overwritten** - only updated in-place.

**Why 4 Collections?**
- **fixtures_staging**: Waiting to activate
- **fixtures_live**: Raw API data (temporary, for comparison only)
- **fixtures_active**: Enhanced events (never replaced, only updated)
- **fixtures_completed**: Archive

This prevents data loss - we can compare fresh API data against enhanced data without destroying enhancements.

---

## 🏗️ Multi-Worker Architecture

**Python GIL Limitation**: Python's Global Interpreter Lock limits each process to one CPU core for CPU-bound work (like workflow replay). To utilize multiple cores, we run **multiple worker replicas**.

```
                    ┌─────────────────┐
                    │  Temporal Server │
                    │  (coordination) │
                    └────────┬────────┘
                             │
        ┌───────────┬────────┼────────┬───────────┐
        ▼           ▼        ▼        ▼           ▼
   ┌─────────┐ ┌─────────┐ ┌─────────┐ ┌─────────┐
   │Worker 1 │ │Worker 2 │ │Worker 3 │ │Worker 4 │
   │(Python) │ │(Python) │ │(Python) │ │(Python) │
   │Own GIL  │ │Own GIL  │ │Own GIL  │ │Own GIL  │
   └─────────┘ └─────────┘ └─────────┘ └─────────┘
```

**Configuration** (per worker):
- `max_concurrent_workflow_tasks=10` → 40 total across 4 workers
- `max_concurrent_activities=30` → 120 total across 4 workers
- `sticky_queue_schedule_to_start_timeout=10s` → Default, works well with low contention

**Auto-Scaling**:

The **Scaler Service** monitors Temporal task queue depth and automatically scales workers and Twitter instances:

```
┌─────────────────────────────────────────────────────────────────────┐
│                     docker compose up -d                             │
├─────────────────────────────────────────────────────────────────────┤
│  Starts: postgres, mongo, temporal, minio, scaler                    │
│  Does NOT start: workers, twitter (they use profiles: ["managed"])   │
└──────────────────────────────┬──────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        SCALER SERVICE                                │
├─────────────────────────────────────────────────────────────────────┤
│  1. Auto-starts minimum instances (2 workers, 2 twitter)             │
│  2. Query Temporal describe_task_queue API (every 30s)               │
│  3. Calculate: backlog_per_worker = pending_tasks / running_workers  │
│  4. Scale up if: backlog_per_worker > 5                              │
│  5. Scale down if: backlog_per_worker < 2 (with 60s cooldown)        │
│  6. Uses python-on-whales with profiles: ["managed"]                 │
└─────────────────────────────────────────────────────────────────────┘
```

| Config | Default | Description |
|--------|---------|-------------|
| MIN_INSTANCES | 2 | Minimum workers/Twitter instances |
| MAX_INSTANCES | 8 | Maximum workers/Twitter instances |
| SCALE_UP_THRESHOLD | 5 | Scale up when > 5 pending tasks/worker |
| SCALE_DOWN_THRESHOLD | 2 | Scale down when < 2 pending tasks/worker |
| CHECK_INTERVAL | 30s | How often to check metrics |
| SCALE_COOLDOWN | 60s | Minimum time between scaling actions |

```bash
# Start entire stack (one command)
docker compose up -d

# Manual scaling (if needed, uses managed profile)
docker compose --profile managed up -d worker-3 twitter-3
docker compose --profile managed stop worker-3
```

**Why This Is Safe** (no race conditions):
| Guarantee | Scope | Enforced By |
|-----------|-------|-------------|
| Workflow ID Uniqueness | Only one running per ID | Temporal Server |
| Task Exclusivity | Each task goes to ONE worker | Temporal Server |
| Signal Ordering | FIFO within a workflow | Temporal Server |
| Sticky Queue | Same workflow prefers same worker | Worker cache (optimization) |

Child workflows (Twitter→Download→Upload) can run on **different workers** - this is safe because:
1. `UploadWorkflow` ID is `upload-{event_id}` - Temporal prevents duplicates
2. Signals are delivered in order regardless of which worker sends them
3. All serialization is at Temporal Server level, not worker level

---

## 📊 Data Flow

```
                              API-Football
                                   │
                    ┌──────────────┴──────────────┐
                    ▼                              ▼
            IngestWorkflow                  MonitorWorkflow
           (Daily 00:05 UTC)               (Every 30 seconds)
    (Fetches today+tomorrow+day_after)              │
                    │                              ▼
                    ▼                              ▼
           fixtures_staging ──────────────► fixtures_active
           (TBD, NS fixtures)    activate    (live matches)
                                                   │
                                                   ▼
                                            fixtures_live
                                          (temp API buffer)
                                                   │
                                          ┌────────┴────────┐
                                          ▼                 ▼
                                    Compare IDs      On _monitor_complete
                                    (set ops)              │
                                          │                ▼
                                    Increment       TwitterWorkflow
                                    counters        (fire-and-forget)
                                                          │
                                                          ▼
                                                   DownloadWorkflow
                                                   (per attempt)
                                                          │
                                                          ▼
                                                    UploadWorkflow
                                                   (serialized per event)
                                                          │
                                                          ▼
                                                      MinIO S3
                                                          │
                                                          ▼
                                            When fixture FT + all complete
                                                          │
                                                          ▼
                                              fixtures_completed
```

---

## 🔄 Workflow Hierarchy

```
┌─────────────────────────────────────────────────────────────────────┐
│                        SCHEDULED WORKFLOWS                           │
├─────────────────────────────────────────────────────────────────────┤
│  IngestWorkflow (00:05 UTC)     MonitorWorkflow (Every 30s)         │
│         │                                │                           │
│    Fetch 3 days of fixtures         Poll API                        │
│    (today+tomorrow+day_after)       Debounce events                  │
│    Skip existing fixtures           Trigger Twitter on stable        │
│    Pre-cache RAG aliases                                             │
│    Route by status                                                   │
└──────────────────────────────────────┬──────────────────────────────┘
                                       │
                                       ▼ (FIRE-AND-FORGET, ABANDON)
┌─────────────────────────────────────────────────────────────────────┐
│                TwitterWorkflow (~10 minutes)                         │
├─────────────────────────────────────────────────────────────────────┤
│  1. Resolve team_aliases (cache lookup OR RAG pipeline)              │
│     └── get_cached_team_aliases OR get_team_aliases (Wikidata+LLM)  │
│  2. FOR attempt IN [1..10]:                                          │
│     → update_twitter_attempt(attempt)                                │
│     → Search each alias: "Salah Liverpool", "Salah LFC", ...        │
│     → Dedupe videos                                                  │
│     → IF videos: start DownloadWorkflow (BLOCKING child)             │
│     → ELSE: increment_twitter_count (no download to do it)           │
│     → IF attempt < 10: workflow.sleep(1 minute) ← DURABLE TIMER     │
│  Downloads set _twitter_complete when count reaches 10               │
└──────────────────────────────────────┬──────────────────────────────┘
                                       │
                                       ▼ (BLOCKING child workflow)
┌─────────────────────────────────────────────────────────────────────┐
│                        DownloadWorkflow                              │
├─────────────────────────────────────────────────────────────────────┤
│  0. check_event_exists (VAR check - abort if event removed)          │
│  PARALLEL: Download videos via Twitter syndication API               │
│  1. MD5 batch dedup (within downloaded batch only)                   │
│  2. AI validation (reject non-football + phone-TV recordings)        │
│     Uses 2/3 majority tiebreaker for both checks                     │
│  PARALLEL: Compute perceptual hash (heartbeat every 5 frames)        │
│  3. Queue videos for upload (signal-with-start to UploadWorkflow)    │
│  4. IF NO videos to upload: increment_twitter_count                  │
│     (UploadWorkflow handles increment when videos ARE queued)        │
└──────────────────────────────────────┬──────────────────────────────┘
                                       │
                                       ▼ (SIGNAL-WITH-START, serialized)
┌─────────────────────────────────────────────────────────────────────┐
│                  UploadWorkflow (ID: upload-{event_id})              │
├─────────────────────────────────────────────────────────────────────┤
│  ** SERIALIZED via Signal-with-Start pattern **                      │
│  - Multiple DownloadWorkflows signal the SAME UploadWorkflow         │
│  - Videos queued via add_videos signal (FIFO deque)                  │
│  - Workflow idles for 5 min waiting for more signals                 │
│  0. Abort if event removed (VAR check via fetch_event_data)          │
│  1. Receive videos via signal → add to pending queue                 │
│  2. Process batches: fetch S3 state, dedup, upload                   │
│  3. Remove old MongoDB entries ONLY after successful upload          │
│  4. Update MongoDB + recalculate video ranks                         │
│  5. Cleanup individual files after successful upload                 │
│  6. increment_twitter_count (each batch = one Twitter attempt)       │
│  7. Wait for more signals or timeout after 5 min idle                │
└─────────────────────────────────────────────────────────────────────┘
```

**Key Architecture Points:**
- **Monitor → Twitter**: Fire-and-forget (ABANDON policy)
- **Twitter → Download**: BLOCKING child (waits for completion)
- **Download → Upload**: Signal-with-start pattern with deterministic ID `upload-{event_id}`
- **Race condition prevention**: Multiple DownloadWorkflows signal ONE UploadWorkflow per event
- **FIFO queue**: UploadWorkflow processes batches in signal order via deque
- **`_twitter_complete`**: Set by UploadWorkflow when count reaches 10 (ensures uploads finish first)
- **Safe replacement**: MongoDB entries only removed AFTER successful S3 upload
- **Temp cleanup**: Individual files deleted after upload; fixture temp dirs cleaned on completion
- **Heartbeat-based timeouts**: Long activities use `heartbeat_timeout` instead of arbitrary `execution_timeout`
- **Comprehensive logging**: Every failure path logged with `[WORKFLOW]` prefix

---

## 🎬 Video Pipeline

### Twitter → Download → Upload → S3 Flow

```
TwitterWorkflow (per event, resolves aliases then searches)
    │
    ├── Resolve aliases (cache OR RAG pipeline) ← BLOCKING
    │   └── ["Liverpool", "LFC", "Reds"]
    │
    ├── Attempt 1 (immediate):
    │   ├── Search "Salah Liverpool" → 3 videos
    │   ├── Search "Salah LFC" → 2 videos
    │   ├── Search "Salah Reds" → 1 video  
    │   ├── Dedupe (by URL) → 4 unique
    │   ├── Save to _discovered_videos
    │   └── START DownloadWorkflow (BLOCKING) → waits for completion
    │         │
    │         ├── Download 4 videos in parallel
    │         ├── MD5 batch dedup (within batch)
    │         ├── AI validation → 3 pass
    │         ├── Generate perceptual hashes
    │         └── START UploadWorkflow (BLOCKING, ID: upload-{event_id})
    │               │
    │               └── ** SERIALIZED per event **
    │                   ├── Fetch FRESH S3 state
    │                   ├── MD5 dedup vs S3
    │                   ├── Perceptual dedup vs S3
    │                   ├── Upload new/replace worse
    │                   └── Update MongoDB + recalculate ranks
    │
    ├── sleep(1 min) ← Durable timer
    │
    ├── Attempt 2:
    │   ├── Same 3 searches (exclude already-found URLs)
    │   ├── 1 new video found
    │   └── START DownloadWorkflow (BLOCKING) → UploadWorkflow
    │
    ... (attempts 3-10 similar) ...
    │
    └── Attempt 10:
        └── 0 new videos → increment_twitter_count (no download to do it)
```

**Race Condition Prevention (via Signal-with-Start Pattern):**
- Multiple DownloadWorkflows may find videos for the same event simultaneously
- Each signals UploadWorkflow via `queue_videos_for_upload` activity
- Activity uses Temporal Client API's signal-with-start:
  - If UploadWorkflow not running: START it AND signal with videos
  - If UploadWorkflow already running: just SIGNAL with videos (FIFO queue)
- UploadWorkflow ID `upload-{event_id}` is namespace-scoped (global)
- Videos processed in FIFO order via internal deque
- No "Workflow execution already started" errors - signals always succeed

### Perceptual Hash Deduplication

**Problem**: Same video at different resolutions/bitrates = different file hashes but same content. Additionally, videos of the same goal often have different start/end times (offsets).

**Solution**: Dense sampling with histogram equalization
- Sample frames every **0.25 seconds** throughout video
- Apply **histogram equalization** to normalize contrast/brightness
- Compute **dHash** (64-bit difference hash) for each frame
- Store all hashes: `dense:0.25:<ts>=<hash>,<ts>=<hash>,...`

**MongoDB is Source of Truth**: Video metadata (including full perceptual hashes) is stored in MongoDB's `_s3_videos` array. S3 object metadata has a ~100 character limit per field and will truncate long hashes. Deduplication reads from MongoDB only.

**Offset-Tolerant Matching**:
- Different clips of the same goal may start at different times
- Algorithm tries all possible time offsets between videos
- Requires **3 consecutive frames** to match at a consistent offset
- Each frame must have Hamming distance ≤10 bits (of 64)

**Why 3 Consecutive Frames?**
Single-frame matching causes false positives between similar content (e.g., goals scored 1 minute apart in same match). Requiring 3 consecutive frames ensures the videos share actual continuous content.

**Quality Comparison** (when hashes match):
```python
# Larger file = better quality (higher bitrate/resolution)
if new_file_size > existing_file_size:
    replace_video()  # Delete old, upload new with combined popularity
```

### Popularity Scoring

**Purpose**: Track how many times the same video content appears across sources. Higher popularity = more trusted/validated content.

**Rules**:
1. Every video starts with `popularity = 1` when first seen
2. When duplicates found in same batch, popularities are **summed** (keeps highest quality)
3. When comparing batch winner vs S3, popularities are **combined**:
   - **Batch > S3 quality**: Upload batch video with `batch_popularity + s3_popularity`, delete S3 video
   - **S3 > Batch quality**: Keep S3 video, bump popularity to `s3_popularity + batch_popularity`

**Example Flow**:
```
Batch: Video A (720p, pop=1), Video B (1080p, pop=1), Video C (480p, pop=1) - all same content
S3: Video D (360p, pop=2) - same content

Phase 1 (Batch Dedup):
├── A arrives: pop=1
├── B arrives: matches A, B is larger → keep B, pop=1+1=2, delete A
└── C arrives: matches B, B is larger → keep B, pop=2+1=3, delete C

Phase 2 (S3 Dedup):
└── B (10MB, pop=3) vs D (1MB, pop=2)
    → B is larger → REPLACE
    → Upload B with pop=3+2=5, delete D
```

### Duration Filtering

Videos outside the >3s to 60s range are filtered:
- **≤3s**: Usually just celebrations or snippets, not full goal replays
- **>60s**: Usually compilations or full match highlights

Filtered videos still have their URLs tracked to prevent re-download attempts.

---

## 🗄️ Collection Schemas

### fixtures_staging

Fixtures waiting to start (status TBD, NS).

```json
{
  "_id": 5000,
  "fixture": {
    "id": 5000,
    "date": "2025-11-24T15:00:00Z",
    "status": {"short": "TBD"}
  },
  "teams": {
    "home": {"id": 40, "name": "Liverpool"},
    "away": {"id": 50, "name": "Man City"}
  },
  "league": {"id": 39, "name": "Premier League"}
}
```

### fixtures_live

**Temporary storage** for raw API data. Overwritten each poll. **Filtered to Goals only**.

```json
{
  "_id": 5000,
  "stored_at": "2025-11-24T15:25:00Z",
  "fixture": {...},
  "teams": {...},
  "events": [
    {
      "player": {"id": 234, "name": "D. Szoboszlai"},
      "team": {"id": 40, "name": "Liverpool"},
      "type": "Goal",
      "detail": "Normal Goal",
      "time": {"elapsed": 23},
      "_event_id": "5000_40_234_Goal_1"
    }
  ]
}
```

### fixtures_active

Enhanced fixtures with video tracking. Events array **grows incrementally**, **never replaced**.

```json
{
  "_id": 5000,
  "activated_at": "2025-11-24T15:00:00Z",
  "_last_activity": "2025-11-24T16:45:00Z",
  "fixture": {...},
  "teams": {...},
  "events": [
    {
      // ========== RAW API FIELDS ==========
      "player": {"id": 234, "name": "D. Szoboszlai"},
      "team": {"id": 40, "name": "Liverpool"},
      "type": "Goal",
      "time": {"elapsed": 23},
      
      // ========== ENHANCED FIELDS ==========
      "_event_id": "5000_40_234_Goal_1",
      "_monitor_count": 5,
      "_monitor_complete": true,
      "_twitter_aliases": ["Liverpool", "LFC", "Reds"],
      "_twitter_count": 3,
      "_twitter_complete": true,
      "_first_seen": "2025-11-24T15:23:45Z",
      "_twitter_search": "Szoboszlai Liverpool",
      
      // ========== VIDEO TRACKING ==========
      "_discovered_videos": [
        {
          "video_page_url": "https://x.com/i/status/123",
          "tweet_url": "https://x.com/user/status/123",
          "tweet_text": "What a goal!",
          "discovered_at": "2025-11-24T15:30:00Z"
        }
      ],
      "_s3_videos": [
        {
          "s3_url": "http://minio:9000/footy/...",
          "s3_key": "5000/5000_40_234_Goal_1/abc123.mp4",
          "perceptual_hash": "15.2_abc_def_ghi",
          "width": 1920,
          "height": 1080,
          "bitrate": 5000000,
          "file_size": 15000000,
          "source_url": "https://x.com/i/status/123"
        }
      ]
    }
  ]
}
```

### fixtures_completed

Archive with all enhancements intact. fixtures_live entry deleted.

```json
{
  "_id": 5000,
  "completed_at": "2025-11-24T16:50:00Z",
  "_last_activity": "2025-11-24T16:45:00Z",
  "fixture": {...},
  "events": [...]
}
```

---

## 🔄 Workflow Details

### 1. IngestWorkflow (Daily 00:05 UTC)

**Purpose**: Fetch fixtures for today + tomorrow + day after, route by status, cleanup old data (14-day retention)

| Activity | Purpose | Retries |
|----------|---------|---------|
| `fetch_todays_fixtures` | Fetch fixtures for a date from API-Football | 3x, 2.0x backoff from 1s |
| `fetch_fixtures_by_ids` | Manual ingest by ID | 3x, 2.0x backoff from 1s |
| `categorize_and_store_fixtures` | Route by status, skip existing | 3x, 2.0x backoff from 1s |
| `cleanup_old_fixtures` | Delete fixtures >14 days old | 2x |

**3-Day Fetch**: Fetches today + tomorrow + day after (UTC) to handle timezone edge cases. Allows frontend to show "tomorrow" fixtures for users in any timezone.

**Duplicate Detection**: Fixtures already in staging/active/completed are skipped (monitor handles updates).

**Dynamic Team Tracking**: Tracks ~96 teams from top 5 leagues (fetched from API, cached 24h) plus 15 national teams.

**Retention Policy**: Keeps 14 days of fixture history. Since ingestion runs at 00:05 UTC (before today's matches), "Day 1" = yesterday. Deletes both MongoDB documents and S3 videos.

### 2. MonitorWorkflow (Every 30 Seconds)

**Purpose**: Activate fixtures, detect events, trigger RAG for stable events

| Activity | Purpose | Retries |
|----------|---------|---------|
| `fetch_staging_fixtures` | Get staging fixture data | 3x |
| `process_staging_fixtures` | Update staging from API | 3x |
| `activate_pending_fixtures` | Move staging → active | 2x |
| `fetch_active_fixtures` | Batch fetch from API | 3x |
| `store_and_compare` | Filter events, store in live | 3x, 2.0x backoff |
| `process_fixture_events` | Increment counts, detect stable | 3x |
| `complete_fixture_if_ready` | Move to completed | 3x, 2.0x backoff |
| `notify_frontend_refresh` | SSE broadcast | 1x |

**Key Change**: Monitor now triggers **TwitterWorkflow** directly when events reach `_monitor_complete=true`. TwitterWorkflow resolves aliases at start (cache or RAG pipeline).

### 3. TwitterWorkflow (Per Stable Event)

**Purpose**: Resolve team aliases, search Twitter for event videos, manage retries internally

| Activity | Purpose | Retries |
|----------|---------|---------|
| `get_cached_team_aliases` | Fast MongoDB cache lookup | 2x |
| `get_team_aliases` | Full RAG pipeline (Wikidata + LLM) | 2x |
| `save_team_aliases` | Store to event in MongoDB | 2x |
| `check_event_exists` | VAR check - abort if removed | 3x |
| `get_twitter_search_data` | Get existing URLs | 2x |
| `execute_twitter_search` | POST to Firefox | 3x, 1.5x from 10s |
| `save_discovered_videos` | Persist to MongoDB | 3x, 2.0x |

**Alias Resolution (at workflow start):**
1. Check `team_aliases` MongoDB cache by team_id
2. If miss: Call API-Football `/teams?id={id}` to get `team.national` boolean
3. Query Wikidata for team QID and aliases
4. Preprocess aliases to single words (filter junk, split phrases)
5. LLM selects best words for Twitter search (llama.cpp server with Qwen3 model)
6. Add nationality adjectives for national teams ("Belgian", "French")
7. Cache result with `national` boolean and `created_at` timestamp

**Key Feature**: Uses `workflow.sleep(1 minute)` between attempts - durable timer survives restarts.

**Note**: `_twitter_complete` is set by DownloadWorkflow via `increment_twitter_count`, not by TwitterWorkflow.

### 4. DownloadWorkflow (Per Twitter Attempt)

**Purpose**: Download, filter, validate, hash videos - delegate upload to UploadWorkflow

| Activity | Purpose | Retries |
|----------|---------|--------|
| `check_event_exists` | VAR check - abort if removed | 1x |
| `download_single_video` | Download ONE video | 3x, 2.0x from 2s |
| `validate_video_is_soccer` | AI vision validates soccer content | 4x |
| `generate_video_hash` | Perceptual hash (heartbeat) | 2x |
| `cleanup_download_temp` | Clean temp files if no videos | 2x |
| `increment_twitter_count` | Increment count, set complete at 10 | 5x |

**AI Video Validation**:
- Extracts a frame from downloaded video
- Sends to vision model (Qwen3-VL-8B via llama.cpp)
- Asks: "Is this a soccer/football match?"
- Only uploads if validated as soccer content
- Uses fail-closed policy: if AI unavailable, skip video (don't upload unvalidated)

### 5. UploadWorkflow (Serialized Per Event)

**Purpose**: S3 deduplication and upload - SERIALIZED via deterministic workflow ID

| Activity | Purpose | Retries |
|----------|---------|--------|
| `fetch_event_data` | Get existing S3 videos (also VAR check) | 3x |
| `deduplicate_by_md5` | Fast exact duplicate removal | 2x |
| `deduplicate_videos` | Perceptual hash dedup vs S3 | 3x |
| `bump_video_popularity` | Increment popularity on match | 2x |
| `update_video_in_place` | Atomic in-place update for replacements | 3x |
| `upload_single_video` | Upload ONE video to S3 | 3x |
| `save_video_objects` | Save to MongoDB _s3_videos (new videos) | 3x |
| `recalculate_video_ranks` | Recompute video ranks | 2x |
| `cleanup_upload_temp` | Remove temp directory | 2x |

**KEY DESIGN**: Workflow ID is `upload-{event_id}`. Temporal ensures only ONE workflow 
with this ID runs at a time. Multiple DownloadWorkflows calling UploadWorkflow for 
the same event will QUEUE - each sees fresh S3 state when it runs.

---

## � Frontend Notifications (SSE Broadcast)

The `notify_frontend_refresh` activity triggers an SSE broadcast to all connected browser clients, telling them to refetch data.

### When Notifications Happen

| Workflow | Trigger Point | Condition |
|----------|---------------|-----------|
| **MonitorWorkflow** | After processing active fixtures | Only if new Twitter workflows were started |
| **MonitorWorkflow** | End of every 30s cycle | Always (ensures UI stays fresh) |
| **TwitterWorkflow** | After all 10 search attempts complete | Always |
| **UploadWorkflow** | After each batch upload completes | Only if videos were added or updated |
| **IngestWorkflow** | After ingesting fixtures | Always |

### Video Upload Notification Order

When a video is uploaded, the notification happens **after** all processing is complete:

```
1. Upload to S3
2. Save to MongoDB (new videos) OR update in-place (replacements)
3. Recalculate video ranks ← Ensures rank is correct
4. Notify frontend ← Browser sees video with proper rank
```

This ordering prevents the brief "rank=0" display that would occur if we notified before rank calculation.

### Notification Frequency

- **Normal operation**: ~every 30 seconds from MonitorWorkflow
- **During active events**: Additional notifications from UploadWorkflow after each batch
- **Multiple rapid uploads**: Each batch triggers its own notification

---

## �📝 Event Enhancement Fields

| Field | Type | Set By | Purpose |
|-------|------|--------|---------|
| `_event_id` | string | Monitor | Unique: `{fixture}_{team}_{player}_{type}_{seq}` |
| `_monitor_count` | int | Monitor | Debounce count (0=unknown player, 1-3=known) |
| `_monitor_complete` | bool | Monitor | true when `_monitor_count >= 3` |
| `_twitter_aliases` | array | TwitterWorkflow | Team search variations |
| `_twitter_count` | int | DownloadWorkflow | Completed attempts count (0-10) |
| `_twitter_complete` | bool | DownloadWorkflow | true when count reaches 10 |
| `_first_seen` | datetime | Monitor | When event first appeared |
| `_twitter_search` | string | Monitor | `{player_last} {team_name}` |
| `_removed` | bool | Monitor | true if VAR disallowed |
| `_discovered_videos` | array | Twitter | Video URLs from searches |
| `_s3_videos` | array | Download | Uploaded videos with metadata |

---

## 🎯 Key Design Decisions

### Why fixtures_live?
Store raw API data temporarily for comparison without destroying enhancements.

### Why alias resolution in TwitterWorkflow?
Previously RAGWorkflow was a separate fire-and-forget intermediary. Now TwitterWorkflow 
resolves aliases at its start (cache lookup or RAG pipeline). This eliminates a 
double-fire-and-forget chain that caused duplicate workflows.

### Why self-managing TwitterWorkflow?
Durable timers allow 1-minute spacing between attempts, decoupled from Monitor's 30-second poll.

### Why UploadWorkflow with deterministic ID?
Multiple DownloadWorkflows may find videos for the same event simultaneously (different Twitter 
search attempts). UploadWorkflow with ID `upload-{event_id}` serializes S3 operations - Temporal 
ensures only one runs at a time, eliminating race conditions. Each sees fresh S3 state.

### Why 10 Twitter attempts with 1-min spacing?
Goal videos appear over 5-15 minutes. More frequent searches = fresher videos = better content. 
Blocking downloads ensure completion tracking is reliable.

### Why perceptual hashing?
Same video at different bitrates = different file hashes. Perceptual hash catches duplicates.

### Why quality comparison on S3?
Replace 720p with 1080p if same content found later.

### Why `$max` for `_last_activity`?
Ensures timestamp only moves forward (handles out-of-order processing).

### Why per-video retry?
If 3/5 videos succeed, those are preserved. Partial success beats total failure.

---

## 🚀 Testing

### Run a Test Fixture
```bash
docker exec found-footy-worker python /workspace/tests/workflows/test_pipeline.py --fixture-id 1469132
```

### Check Video Pipeline
```bash
docker compose -f docker-compose.dev.yml logs -f worker | grep -E "(Download|Upload|S3|quality|phash)"
```

### Verify S3 Videos
```bash
docker exec found-footy-worker python -c "
from src.data.s3_store import FootyS3Store
s3 = FootyS3Store()
objs = s3.s3_client.list_objects_v2(Bucket='footy-videos', Prefix='')
for obj in objs.get('Contents', []):
    print(f\"{obj['Key']} ({obj['Size']/1024/1024:.2f} MB)\")
"
```

---

## 📊 Collection Lifecycle

```
fixtures_staging: Hours to days (until start time)
fixtures_live: ~1 minute (overwritten each poll)
fixtures_active: ~90 minutes (fixture duration)
fixtures_completed: Forever (archive)
```

---

## 🔍 Debugging Tips

### Check Workflow Status
```
Temporal UI: http://localhost:3100
```

### Check MongoDB
```
Mongoku: http://localhost:3101
```

### Multi-Worker Operations
```bash
# Check all workers are running
docker ps --filter "name=found-footy-prod-worker"

# Logs from all workers
for i in 1 2 3 4; do echo "=== Worker $i ===" && docker logs found-footy-prod-worker-$i --since 30s 2>&1 | tail -10; done

# Check for "Task not found" errors (indicates replay issues)
docker logs found-footy-prod-worker-1 2>&1 | grep -c "Task not found"

# Scale workers up/down
docker compose up -d --scale worker=8
```

### Common Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| Fixture stuck in active | Events missing `_twitter_complete` | Check TwitterWorkflow in Temporal UI |
| Videos not uploading | S3 connection failed | Check MinIO is running |
| Duplicate videos | Upload serialization failed | Check UploadWorkflow logs |
| Twitter search empty | Browser session expired | Re-login via VNC (port 4103) |
| Alias resolution slow | Cache miss, RAG pipeline running | Normal for first-time teams |
