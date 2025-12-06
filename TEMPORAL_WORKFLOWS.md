# Found Footy - Temporal Workflow Architecture

## Overview

This system uses Temporal.io to orchestrate the discovery, tracking, and archival of football goal videos from social media. The architecture consists of **4 workflows** that form a parent-child cascade, managing the full pipeline from fixture ingestion to video download.

---

## Workflow Hierarchy

```mermaid
graph TB
    subgraph "Scheduled Workflows"
        I[IngestWorkflow<br/>Daily 00:05 UTC]
        M[MonitorWorkflow<br/>Every Minute]
    end
    
    subgraph "Child Workflows"
        T[TwitterWorkflow<br/>Per Stable Event]
        D[DownloadWorkflow<br/>Per Video Set]
    end
    
    I -->|Fetches Fixtures| DB[(MongoDB)]
    M -->|Processes events inline| M
    M -->|Triggers per stable event| T
    T -->|Triggers if videos found| D
    D -->|Uploads| S3[S3 Storage]
    
    style I fill:#e1f5ff
    style M fill:#e1f5ff
    style T fill:#ffe1e1
    style D fill:#e1ffe1
```

---

## Workflow Naming Convention

All workflows use human-readable IDs for easy debugging in Temporal UI:

| Workflow | ID Format | Example |
|----------|-----------|---------|
| **IngestWorkflow** | `ingest-{DD_MM_YYYY}` | `ingest-05_12_2024` |
| **MonitorWorkflow** | `monitor-{DD_MM_YYYY}-{HH:MM}` | `monitor-05_12_2024-15:23` |
| **TwitterWorkflow** | `twitter-{Team}-{LastName}-{min}[+extra]-{event_id}` | `twitter-Liverpool-Salah-45+3min-123456_40_306_Goal_1` |
| **DownloadWorkflow** | `download-{Team}-{LastName}-{count}vids-{event_id}` | `download-Liverpool-Salah-3vids-123456_40_306_Goal_1` |

**Notes:**
- Team names use full name with underscores for spaces/dots (A.C. Milan → A_C__Milan)
- Player names use last name only
- Minutes include extra time when present (45+3 for 45' + 3' added time)
- Event IDs are unique: `{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}`

---

## MongoDB Collection Architecture

The system uses a **4-collection architecture** designed for robust change tracking:

```mermaid
graph LR
    subgraph "Collection Flow"
        ST[fixtures_staging<br/>TBD, NS]
        LI[fixtures_live<br/>Temporary Buffer]
        AC[fixtures_active<br/>Enhanced Events]
        CO[fixtures_completed<br/>FT, AET, PEN]
    end
    
    API[API-Football] -->|IngestWorkflow| ST
    ST -->|MonitorWorkflow<br/>activate_fixtures| AC
    API -->|MonitorWorkflow<br/>fetch + store| LI
    LI -->|Compare via sets| AC
    AC -->|complete_fixture| CO
    
    style ST fill:#e1f5ff
    style LI fill:#fff4e1
    style AC fill:#e1ffe1
    style CO fill:#f0f0f0
```

### Collection Purposes

| Collection | Purpose | Update Pattern |
|------------|---------|----------------|
| **fixtures_staging** | Pre-match fixtures (TBD, NS) | Insert/Delete |
| **fixtures_live** | Temporary comparison buffer | Overwrite each poll |
| **fixtures_active** | In-progress with enhancements | Incremental only |
| **fixtures_completed** | Archive of finished matches | Insert only |

---

## 1. IngestWorkflow

**Schedule**: Daily at 00:05 UTC (PAUSED by default)  
**Purpose**: Fetch today's fixtures and route to correct collections

```mermaid
sequenceDiagram
    participant S as Scheduler
    participant IW as IngestWorkflow
    participant API as API-Football
    participant DB as MongoDB
    
    S->>IW: Trigger daily 00:05 UTC
    IW->>API: GET /fixtures?date=today
    API-->>IW: List of fixtures
    
    loop For each fixture
        alt Status: TBD, NS
            IW->>DB: Store in fixtures_staging
        else Status: LIVE, 1H, HT, 2H
            IW->>DB: Store in fixtures_active
        else Status: FT, AET, PEN
            IW->>DB: Store in fixtures_completed
        end
    end
```

### Activities
1. **fetch_todays_fixtures**: GET request to API-Football
2. **categorize_and_store_fixtures**: Route fixtures based on status

---

## 2. MonitorWorkflow

**Schedule**: Every minute (ENABLED)  
**Purpose**: Poll active fixtures, process events inline, trigger Twitter for stable events

This workflow contains the **debounce logic inline** - no separate EventWorkflow needed.

```mermaid
sequenceDiagram
    participant S as Scheduler
    participant MW as MonitorWorkflow
    participant Act as Activities
    participant Live as fixtures_live
    participant Active as fixtures_active
    participant TW as TwitterWorkflow
    
    S->>MW: Trigger every minute
    
    MW->>Act: activate_fixtures()
    Act->>Active: Move staging → active
    
    MW->>Act: fetch_active_fixtures()
    Act-->>MW: List of fixture data
    
    loop For each fixture
        MW->>Act: store_and_compare(fixture_id, data)
        Act->>Live: Store filtered events<br/>with _event_id
        Act-->>MW: {new, incomplete, removed}
        
        alt Has events to process
            MW->>Act: process_fixture_events(fixture_id)
            Note over Act: Pure set operations
            Act->>Active: Add new (count=1)<br/>Increment matching<br/>Mark removed
            Act-->>MW: {twitter_triggered: [...]}
            
            loop For each stable event
                MW->>TW: Start TwitterWorkflow
            end
        end
        
        MW->>Act: sync_fixture_metadata(fixture_id)
        MW->>Act: complete_fixture_if_ready(fixture_id)
    end
```

### Activities

#### 1. **activate_fixtures**
Moves fixtures from `staging` → `active` when start time is reached.

#### 2. **fetch_active_fixtures**
Batch fetch all active fixtures from API-Football.

#### 3. **store_and_compare**
- Filters events to Goals only
- Generates `_event_id` per event
- Stores in `fixtures_live` (overwrites)
- Quick comparison to determine if processing needed

#### 4. **process_fixture_events** (Core Debounce Logic)

This activity implements **set-based debounce**:

```python
# 1. Build sets from event IDs
live_ids = {e["_event_id"] for e in live_events}
active_ids = {e["_event_id"] for e in active_events}

# 2. Compute deltas
new_ids = live_ids - active_ids       # NEW events
removed_ids = active_ids - live_ids   # VAR disallowed
matching_ids = live_ids & active_ids  # Existing events

# 3. Process each category
for event_id in new_ids:
    add_to_active(stable_count=1)

for event_id in removed_ids:
    mark_removed()

for event_id in matching_ids:
    if not debounce_complete:
        increment_stable_count()
        if stable_count >= 3:
            mark_debounce_complete()
            twitter_triggered.append(event_id)
```

**Why this works**: Event ID format includes player_id, so VAR player changes create a different event_id automatically. No hash comparison needed!

#### 5. **sync_fixture_metadata**
Updates fixture-level data (score, status) from live to active.

#### 6. **complete_fixture_if_ready**
Moves fixture to `completed` when all criteria met.

---

## 3. TwitterWorkflow

**Trigger**: Child workflow per stable event  
**Purpose**: Search Twitter for event videos, trigger Download if found

Uses 3 granular activities for proper retry semantics:

```mermaid
sequenceDiagram
    participant MW as MonitorWorkflow
    participant TW as TwitterWorkflow
    participant A1 as get_twitter_search_data
    participant A2 as execute_twitter_search
    participant A3 as save_twitter_results
    participant TS as Twitter Service
    participant DB as MongoDB
    participant DW as DownloadWorkflow
    
    MW->>TW: Start child workflow
    
    TW->>A1: get_twitter_search_data
    A1->>DB: Get _twitter_search field
    A1->>DB: Mark twitter_started
    A1-->>TW: {twitter_search: "Salah Liverpool"}
    
    TW->>A2: execute_twitter_search
    Note over A2: 3 retries, 10s interval
    A2->>TS: POST http://twitter:8888/search
    Note over TS: Firefox browser automation<br/>120s timeout
    TS-->>A2: {videos: [...]}
    A2-->>TW: {videos: [...]}
    
    TW->>A3: save_twitter_results
    A3->>DB: Save discovered_videos
    A3->>DB: Mark twitter_complete
    A3-->>TW: {saved: true}
    
    alt video_count > 0
        TW->>DW: Start DownloadWorkflow
    end
```

### 3 Granular Activities

| Activity | Purpose | Timeout | Retries |
|----------|---------|---------|---------|
| `get_twitter_search_data` | Get search query, mark started | 10s | 2 |
| `execute_twitter_search` | POST to Firefox (risky call) | 150s | 3 |
| `save_twitter_results` | Save videos, mark complete | 10s | 2 |

**Why 3 activities?**
- If Twitter search fails → only retry the search (not MongoDB reads)
- If save fails → videos preserved in workflow state
- Clear visibility in Temporal UI of which step failed

---

## 4. DownloadWorkflow

**Trigger**: Child workflow per event with videos  
**Purpose**: Download, deduplicate, upload videos with per-video retry

```mermaid
sequenceDiagram
    participant TW as TwitterWorkflow
    participant DW as DownloadWorkflow
    participant Act as Activities
    participant YT as yt-dlp
    participant S3 as MinIO S3
    
    TW->>DW: Start child workflow
    
    DW->>Act: fetch_event_data(event_id)
    Act-->>DW: {discovered_videos: [...]}
    
    loop For each video URL
        DW->>Act: download_single_video(url, temp_dir)
        Note over Act: 3 retry attempts
        Act->>YT: Download to /tmp
        Act->>Act: Calculate MD5 hash
        Act-->>DW: {file_path, md5_hash, file_size}
    end
    
    DW->>Act: deduplicate_videos(download_results)
    Note over Act: Keep largest per hash
    Act-->>DW: {unique_videos: [...]}
    
    loop For each unique video
        DW->>Act: upload_single_video(file_path, s3_key)
        Note over Act: 3 retry attempts
        Act->>S3: Upload with metadata tags
        Act-->>DW: {s3_url}
    end
    
    DW->>Act: mark_download_complete(event_id, s3_urls)
    Act-->>DW: Cleanup temp dir
```

### 5 Granular Activities

| Activity | Purpose | Timeout | Retries |
|----------|---------|---------|---------|
| `fetch_event_data` | Get event from MongoDB | 10s | 2 |
| `download_single_video` | Download ONE video | 2min | 3 |
| `deduplicate_videos` | Hash-based dedup | 30s | 2 |
| `upload_single_video` | Upload ONE video to S3 | 2min | 3 |
| `mark_download_complete` | Update MongoDB, cleanup | 10s | 2 |

**Why granular?** Each video gets individual retry policy. If 3/5 videos succeed, those are preserved.

### Deduplication Logic

```python
def deduplicate_videos(download_results):
    seen_hashes = {}  # md5_hash -> {file_path, file_size}
    
    for result in download_results:
        hash = result["md5_hash"]
        
        if hash in seen_hashes:
            # Keep larger file (better quality)
            if result["file_size"] > seen_hashes[hash]["file_size"]:
                os.remove(seen_hashes[hash]["file_path"])
                seen_hashes[hash] = result
            else:
                os.remove(result["file_path"])
        else:
            seen_hashes[hash] = result
    
    return list(seen_hashes.values())
```

### S3 Structure

```
bucket/
├── {fixture_id}/
│   ├── {event_id}/
│   │   ├── {md5_hash_1}.mp4
│   │   ├── {md5_hash_2}.mp4
│   │   └── {md5_hash_3}.mp4
```

Each video has metadata tags: `player_name`, `team_name`, `event_id`, `fixture_id`

---

## Event ID Format

The event ID uniquely identifies each goal, including the player:

```
{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}
```

**Example**: `123456_40_306_Goal_1`
- Fixture: 123456
- Team: 40 (Liverpool)
- Player: 306 (Mohamed Salah)
- Type: Goal
- Sequence: 1 (Salah's 1st goal in this match)

**Why include player_id?** VAR player changes create a different event_id automatically. No hash comparison needed!

---

## Event Enhancement Fields

Events in `fixtures_active` have these enhancement fields:

| Field | Purpose | Set By |
|-------|---------|--------|
| `_event_id` | Unique identifier | store_and_compare |
| `_stable_count` | Debounce counter (0-3) | process_fixture_events |
| `_debounce_complete` | true when count >= 3 | process_fixture_events |
| `_twitter_started` | true when search begun | get_twitter_search_data |
| `_twitter_complete` | true when searched | save_twitter_results |
| `_twitter_search` | Search query | process_fixture_events |
| `_score_before` | Score before goal | process_fixture_events |
| `_score_after` | Score after goal | process_fixture_events |
| `_removed` | VAR disallowed | process_fixture_events |
| `discovered_videos` | Video URLs from Twitter | save_twitter_results |
| `s3_urls` | Uploaded video URLs | mark_download_complete |

---

## Error Handling & Retries

### Workflow-Level
- All child workflows inherit parent retry policy
- Failed workflows visible in Temporal UI
- Can retry manually from UI

### Activity-Level

| Activity | Timeout | Max Attempts | Initial Interval |
|----------|---------|--------------|------------------|
| fetch_todays_fixtures | 30s | 3 | 1s |
| activate_fixtures | 30s | 2 | 1s |
| store_and_compare | 10s | 2 | 1s |
| process_fixture_events | 60s | 3 | 1s |
| execute_twitter_search | 150s | 3 | 10s |
| download_single_video | 2min | 3 | 5s |
| upload_single_video | 2min | 3 | 5s |

---

## Development Workflow

### 1. Start Infrastructure
```bash
docker compose -f docker-compose.dev.yml up -d
```

### 2. View Worker Logs
```bash
docker compose -f docker-compose.dev.yml logs -f worker
```

### 3. Monitor in Temporal UI
```
http://localhost:4100
```

### 4. Check MongoDB
```
http://localhost:4101 (MongoDB Express)
```

### 5. Restart Worker (reload code)
```bash
docker compose -f docker-compose.dev.yml restart worker
```

---

## Troubleshooting

### Fixtures Not Moving to Completed
Check that all events have:
- `_debounce_complete: true`
- `_twitter_complete: true`
- `_download_complete: true` (if videos found)

### Twitter Search Failing
1. Check Twitter service is running: `docker logs found-footy-twitter`
2. Check VNC browser: http://localhost:4103
3. Verify Firefox profile exists: `/data/firefox_profile`
4. May need to re-login via VNC GUI

### Downloads Failing
1. Check yt-dlp is installed in worker container
2. Verify video URLs are valid
3. Check MinIO is running and accessible

---

## Summary

| Workflow | Schedule | Purpose | Activities |
|----------|----------|---------|------------|
| IngestWorkflow | Daily 00:05 | Fetch fixtures | 2 |
| MonitorWorkflow | Every minute | Process events | 6 |
| TwitterWorkflow | Per stable event | Find videos | 3 |
| DownloadWorkflow | Per event with videos | Download & upload | 5 |

**Total**: 4 workflows, 14 activities (2 ingest, 6 monitor, 3 twitter, 3 download)

Key architecture decisions:
- ✅ **Set-based debounce** - No hash comparison, player_id in event_id
- ✅ **Inline processing** - No separate EventWorkflow
- ✅ **Granular activities** - Per-video/per-step retry for resilience
- ✅ **Browser automation** - Firefox for Twitter (no API keys)
