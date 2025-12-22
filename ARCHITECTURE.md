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

## 📊 Data Flow

```
                              API-Football
                                   │
                    ┌──────────────┴──────────────┐
                    ▼                              ▼
            IngestWorkflow                  MonitorWorkflow
           (Daily 00:05 UTC)               (Every 1 minute)
                    │                              │
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
                                    Increment       RAGWorkflow
                                    counters        (fire-and-forget)
                                                          │
                                                          ▼
                                                   TwitterWorkflow
                                                   (3 attempts, 3-min spacing)
                                                          │
                                                          ▼
                                                   DownloadWorkflow
                                                   (per attempt)
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
│  IngestWorkflow (00:05 UTC)     MonitorWorkflow (Every 1 min)       │
│         │                                │                           │
│    Fetch fixtures                   Poll API                         │
│    Route by status                  Debounce events                  │
│                                     Trigger RAG on stable            │
└──────────────────────────────────────┬──────────────────────────────┘
                                       │
                                       ▼ (fire-and-forget)
┌─────────────────────────────────────────────────────────────────────┐
│                        RAGWorkflow                                   │
├─────────────────────────────────────────────────────────────────────┤
│  1. get_team_aliases(team_name) → ["Liverpool", "LFC", "Reds"]      │
│  2. save_team_aliases to MongoDB                                     │
│  3. Start TwitterWorkflow (child, waits)                             │
└──────────────────────────────────────┬──────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│                    TwitterWorkflow (Self-Managing)                   │
├─────────────────────────────────────────────────────────────────────┤
│  FOR attempt IN [1, 2, 3]:                                          │
│    → update_twitter_attempt(attempt)                                 │
│    → Search each alias: "Salah Liverpool", "Salah LFC", ...         │
│    → Dedupe videos                                                   │
│    → Start DownloadWorkflow (child, waits)                          │
│    → IF attempt < 3: workflow.sleep(3 minutes) ← DURABLE TIMER      │
│  AFTER attempt 3:                                                    │
│    → mark_event_twitter_complete                                     │
└──────────────────────────────────────┬──────────────────────────────┘
                                       │
                                       ▼
┌─────────────────────────────────────────────────────────────────────┐
│                        DownloadWorkflow                              │
├─────────────────────────────────────────────────────────────────────┤
│  FOR each video URL:                                                 │
│    → Download via yt-dlp                                             │
│    → Filter by duration (5-60s)                                      │
│    → Compute perceptual hash                                         │
│    → Compare quality with existing S3                                │
│    → Upload if new or better quality                                 │
│    → Replace worse quality versions                                  │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 🎬 Video Pipeline

### Twitter → Download → S3 Flow

```
TwitterWorkflow (per event, self-managing)
    │
    ├── Attempt 1 (immediate):
    │   ├── Search "Salah Liverpool" → 3 videos
    │   ├── Search "Salah LFC" → 2 videos
    │   ├── Search "Salah Reds" → 1 video  
    │   ├── Dedupe (by URL) → 4 unique
    │   ├── Save to _discovered_videos
    │   └── DownloadWorkflow → 3 uploaded to S3
    │
    ├── sleep(3 min) ← Durable timer
    │
    ├── Attempt 2:
    │   ├── Same 3 searches (exclude already-found URLs)
    │   ├── 1 new video found
    │   └── DownloadWorkflow → 1 uploaded
    │
    ├── sleep(3 min)
    │
    └── Attempt 3:
        ├── Same searches
        ├── 0 new videos
        └── mark_event_twitter_complete
```

### Perceptual Hash Deduplication

**Problem**: Same video at different resolutions/bitrates = different file hashes but same content

**Solution**: Duration-based perceptual hash
- Extract video duration (0.5s tolerance)
- Extract 3 frames at 1s, 2s, 3s
- Compute dHash (64-bit difference hash) for each frame
- Combine: `{duration:.1f}_{hash1}_{hash2}_{hash3}`

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

Videos outside the 5-60 second range are filtered:
- **< 5s**: Usually just celebrations, not full goal replays
- **> 60s**: Usually compilations or full match highlights

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

**Purpose**: Fetch today's fixtures and route by status

| Activity | Purpose | Retries |
|----------|---------|---------|
| `fetch_todays_fixtures` | Call API-Football | 3x, 2.0x backoff from 1s |
| `categorize_and_store_fixtures` | Route by status | 3x, 2.0x backoff from 1s |

### 2. MonitorWorkflow (Every Minute)

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

**Key Change**: Monitor now triggers **RAGWorkflow** (not TwitterWorkflow) when events reach `_monitor_complete=true`.

### 3. RAGWorkflow (Per Stable Event)

**Purpose**: Resolve team aliases, trigger Twitter workflow

| Activity | Purpose | Retries |
|----------|---------|---------|
| `get_team_aliases` | Query for aliases (stub/LLM) | 2x |
| `save_team_aliases` | Store to MongoDB | 2x |

Then triggers **TwitterWorkflow** as child workflow.

### 4. TwitterWorkflow (Self-Managing, 3 Attempts)

**Purpose**: Search Twitter for event videos, manage retries internally

| Activity | Purpose | Retries |
|----------|---------|---------|
| `update_twitter_attempt` | Set `_twitter_count` | 2x |
| `get_twitter_search_data` | Get existing URLs | 2x |
| `execute_twitter_search` | POST to Firefox | 3x, 1.5x from 10s |
| `save_discovered_videos` | Persist to MongoDB | 3x, 2.0x |
| `mark_event_twitter_complete` | Set completion flag | 3x |

**Key Feature**: Uses `workflow.sleep(3 minutes)` between attempts - durable timer survives restarts.

### 5. DownloadWorkflow (Per Twitter Attempt)

**Purpose**: Download, filter, deduplicate, upload videos

| Activity | Purpose | Retries |
|----------|---------|---------|
| `fetch_event_data` | Get existing S3 metadata | 2x |
| `download_single_video` | Download ONE video | 3x, 2.0x from 2s |
| `deduplicate_videos` | Perceptual hash comparison | 2x |
| `replace_s3_video` | Delete old video when replacing | 3x, 2.0x |
| `upload_single_video` | Upload ONE video to S3 | 3x, 1.5x from 2s |
| `mark_download_complete` | Update MongoDB, cleanup | 3x, 2.0x |

---

## 📝 Event Enhancement Fields

| Field | Type | Set By | Purpose |
|-------|------|--------|---------|
| `_event_id` | string | Monitor | Unique: `{fixture}_{team}_{player}_{type}_{seq}` |
| `_monitor_count` | int | Monitor | Times seen by monitor (1, 2, 3+) |
| `_monitor_complete` | bool | Monitor | true when `_monitor_count >= 3` |
| `_twitter_aliases` | array | RAGWorkflow | Team search variations |
| `_twitter_count` | int | TwitterWorkflow | Current attempt (1, 2, 3) |
| `_twitter_complete` | bool | TwitterWorkflow | true after attempt 3 |
| `_first_seen` | datetime | Monitor | When event first appeared |
| `_twitter_search` | string | Monitor | `{player_last} {team_name}` |
| `_removed` | bool | Monitor | true if VAR disallowed |
| `_discovered_videos` | array | Twitter | Video URLs from searches |
| `_s3_videos` | array | Download | Uploaded videos with metadata |

---

## 🎯 Key Design Decisions

### Why fixtures_live?
Store raw API data temporarily for comparison without destroying enhancements.

### Why RAGWorkflow as intermediary?
Clean separation - alias lookup can be swapped from stub to LLM without touching Twitter logic.

### Why self-managing TwitterWorkflow?
Durable timers allow 3-minute spacing between attempts, decoupled from Monitor's 1-minute poll.

### Why 3 Twitter attempts with 3-min spacing?
Goal videos appear over 5-15 minutes. Early uploads often SD, later often HD.

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
Temporal UI: http://localhost:4100
```

### Check MongoDB
```
MongoDB Express: http://localhost:4101
```

### Common Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| Fixture stuck in active | Events missing `_twitter_complete` | Check TwitterWorkflow in Temporal UI |
| Videos not uploading | S3 connection failed | Check MinIO is running |
| Duplicate videos | Perceptual hash mismatch | Check ffprobe is installed |
| Twitter search empty | Browser session expired | Re-login via VNC (port 4103) |
| RAGWorkflow not starting | Monitor not triggering | Check `_monitor_complete` in MongoDB |
