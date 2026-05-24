# Temporal Workflows — Detailed Specifications

Implementation details for each of the 6 Temporal workflows in Found Footy.

## Worker Registration

**6 workflows, 42 activities** registered per worker:

| Module | Activities | Count |
|--------|-----------|-------|
| ingest | fetch_todays_fixtures, fetch_fixtures_by_ids, categorize_and_store_fixtures, cleanup_old_fixtures | 4 |
| monitor | fetch_staging_fixtures, pre_activate_upcoming_fixtures, fetch_active_fixtures, store_and_compare, process_fixture_events, sync_fixture_metadata, check_twitter_workflow_running, complete_fixture_if_ready, notify_frontend_refresh, register_monitor_workflow | 10 |
| rag | get_team_aliases, save_team_aliases, get_cached_team_aliases | 3 |
| twitter | check_event_exists, get_twitter_search_data, execute_twitter_search, save_discovered_videos, set_monitor_complete, get_download_workflow_count | 6 |
| download | download_single_video, validate_video_is_soccer, generate_video_hash, cleanup_download_temp, queue_videos_for_upload, register_download_workflow, check_and_mark_download_complete | 7 |
| upload | fetch_event_data, deduplicate_by_md5, deduplicate_videos, upload_single_video, update_video_in_place, replace_s3_video, bump_video_popularity, save_video_objects, recalculate_video_ranks, cleanup_individual_files, cleanup_fixture_temp_dirs, cleanup_upload_temp | 12 |

**Schedules** (set up by first worker, idempotent):
- `ingest-daily` — IngestWorkflow at `5 0 * * *` (00:05 UTC)
- `monitor-every-30s` — MonitorWorkflow every 30 seconds, overlap policy: SKIP

---

## IngestWorkflow

**Schedule**: Daily at 00:05 UTC  
**ID**: `ingest-scheduled` (Temporal adds timestamp suffix)

### What it does

1. **Fetch fixtures** — 3-day window (today + tomorrow + day after) for timezone coverage
2. **Lookahead** — If tomorrow has no fixtures (international break), looks ahead up to 30 days
3. **Categorize** — Routes fixtures by status: TBD/NS → staging, live → active, FT → completed
4. **Pre-cache aliases** — Runs RAG pipeline for both teams in each fixture
5. **Cleanup** — Deletes fixtures older than 14 days from MongoDB + S3

### Manual mode

Can be triggered with specific fixture IDs:

```python
IngestWorkflowInput(fixture_ids=[1515514, 1515515])
```

### Activities

| Activity | STC | Retries | Notes |
|----------|-----|---------|-------|
| `fetch_todays_fixtures` | 30s | 3 (2× from 1s) | API-Football batch call |
| `fetch_fixtures_by_ids` | 30s | 3 (2× from 1s) | Manual ingest mode |
| `categorize_and_store_fixtures` | 30s | 3 (2× from 1s) | Skips already-existing fixtures |
| `cleanup_old_fixtures` | 120s | 2 | Deletes from MongoDB + S3 |

Pre-caching uses RAG activities (`get_cached_team_aliases`, `get_team_aliases`, `save_team_aliases`).

---

## MonitorWorkflow

**Schedule**: Every 30 seconds  
**ID**: `monitor-scheduled` (Temporal adds timestamp suffix)  
**Overlap**: SKIP (if previous cycle still running, skip this one)

### What it does

1. **Staging poll** — Check staging fixtures on 15-minute intervals (`:00/:15/:30/:45`)
   - Pre-activate fixtures with kickoff ≤ now + 30 minutes
   - `_last_monitor` tracks per-fixture polling → ~97% fewer API calls
2. **Active poll** — Fetch all active fixtures from API
3. **Process fixtures in parallel** — `asyncio.gather()` across all active fixtures:
   - `store_and_compare` — Store raw data in `fixtures_live`
   - `process_fixture_events` — Set comparison for new/changed/removed events
   - Stable events (3 polls + player known) → spawn TwitterWorkflow
4. **Complete finished fixtures** — Move to `fixtures_completed` when status is terminal + all events done
5. **Cleanup** — Delete temp directories for completed fixtures
6. **Notify frontend** — SSE broadcast

### Activities

| Activity | STC | Retries | Notes |
|----------|-----|---------|-------|
| `pre_activate_upcoming_fixtures` | 30s | 2 | Interval-based staging poll |
| `fetch_active_fixtures` | 15s | 2 | API has 10s internal timeout |
| `store_and_compare` | 10s | 3 (2× from 1s) | Write to `fixtures_live` |
| `process_fixture_events` | 60s | 3 | Event debouncing + trigger logic |
| `check_twitter_workflow_running` | 10s | 2 | Prevents duplicate spawns |
| `complete_fixture_if_ready` | 10s | 3 (2× from 1s) | Active → completed transition |
| `notify_frontend_refresh` | 5–10s | 1 | Best-effort SSE |

### TwitterWorkflow spawn

```python
await workflow.start_child_workflow(
    TwitterWorkflow.run,
    TwitterWorkflowInput(...),
    id=f"twitter-{team}-{player}-{minute}-{event_id}",
    parent_close_policy=ParentClosePolicy.ABANDON,
    task_timeout=timedelta(seconds=60),
    task_queue="found-footy",
)
```

Human-readable IDs: `twitter-Liverpool-Szoboszlai-23min-5000_40_234_Goal_1`

---

## RAGWorkflow

**Trigger**: On-demand from IngestWorkflow (pre-caching)  
**Purpose**: Resolve team aliases via Wikidata SPARQL queries

Pipeline: Team name → Wikidata search → SPARQL for aliases → filter & rank → cache in `team_aliases` collection.

### Activities

| Activity | STC | Retries | Notes |
|----------|-----|---------|-------|
| `get_cached_team_aliases` | 10s | 2 | Cache lookup in MongoDB |
| `get_team_aliases` | 60s | 2 | Full Wikidata pipeline |
| `save_team_aliases` | 10s | 2 | Save to cache |

---

## TwitterWorkflow

**Trigger**: MonitorWorkflow (fire-and-forget, ABANDON)  
**Duration**: ~10–15 minutes  
**ID**: `twitter-{team}-{player}-{minute}-{event_id}`

### What it does

1. **Set `_monitor_complete`** — Proves we're running. If we crash before this, Monitor retries
2. **Resolve aliases** — Cache lookup (`get_cached_team_aliases`) or full RAG pipeline
3. **Search loop** — While `len(_download_workflows) < 10` (max 15 attempts):
   a. Check download count
   b. Check event still exists (VAR abort)
   c. Build OR-query: `"(Florian OR Wirtz) (LFC OR Liverpool)"`
   d. Execute Twitter search (max_age_minutes=3)
   e. Take top 5 longest videos
   f. Start DownloadWorkflow (fire-and-forget, ABANDON)
   g. Wait ~60 seconds (from START of attempt, min 10s)

### Activities

| Activity | STC | Retries | Notes |
|----------|-----|---------|-------|
| `set_monitor_complete` | 30s | 5 (2× from 2s) | Critical — retry hard |
| `get_cached_team_aliases` | 30s | 3 (2× from 2s) | Cache hit path |
| `get_team_aliases` | 90s | 3 (2× from 5s) | RAG fallback |
| `save_team_aliases` | 30s | 2 | Save to event for debugging |
| `get_download_workflow_count` | 30s | 3 (2× from 2s) | While loop condition |
| `check_event_exists` | 30s | 3 (2× from 2s) | VAR check |
| `get_twitter_search_data` | 30s | 3 | Get existing URLs for dedup |
| `execute_twitter_search` | 60s | 3 (1.5× from 10s) | Core search |
| `save_discovered_videos` | 30s | 3 (2× from 2s) | Save URLs to event |

### Player name handling

`extract_player_search_names()` handles accents and hyphens:
- "Florian Wirtz" → `["Florian", "Wirtz"]` → `"(Florian OR Wirtz)"`
- "Mohamed Salah" → `["Salah"]` → `"Salah"`

---

## DownloadWorkflow

**Trigger**: TwitterWorkflow (fire-and-forget, ABANDON)  
**ID**: `download{N}-{team}-{player}-{event_id}`

### Pipeline

```
register → download (parallel) → MD5 batch dedup → AI validate (sequential)
→ timestamp validate → hash (parallel) → signal UploadWorkflow
```

### Step-by-step

1. **Register** — `$addToSet` workflow ID into `_download_workflows`. Must be FIRST
2. **Download** — Parallel per-video downloads (3 retries each, 2× from 2s)
3. **Filter** — Duration 3–60s, aspect ratio ≥ 1.33
4. **MD5 dedup** — Remove exact duplicates within batch (saves AI calls)
5. **AI validation** — Sequential (LLM_SEMAPHORE=2 per worker):
   - Smart 2–3 frame strategy (25%, 75%, tiebreaker at 50%)
   - 5 questions: SOCCER, SCREEN, CLOCK, ADDED, STOPPAGE_CLOCK
   - Reject non-soccer or phone-filming-TV
   - Extract timestamp → `validate_timestamp()` (±3 tolerance)
   - Result: `timestamp_verified`, `extracted_minute`, `timestamp_status`
6. **Hash** — Parallel perceptual hash generation (heartbeat every 5 frames)
7. **Signal Upload** — `queue_videos_for_upload` signals UploadWorkflow via signal-with-start

### Activities

| Activity | STC / Heartbeat | Retries | Notes |
|----------|----------------|---------|-------|
| `register_download_workflow` | 30s | 5 | `$addToSet` — idempotent |
| `check_event_exists` | 30s | 1 | VAR check |
| `download_single_video` | 90s | 3 (2× from 2s) | Per-video, parallel |
| `validate_video_is_soccer` | 90s | 4 (2× from 3s) | AI vision, heartbeat between calls |
| `generate_video_hash` | HB 60s | 2 | Heartbeat every 5 frames |
| `cleanup_download_temp` | 30s | 2 | On failure |
| `queue_videos_for_upload` | 60s | 3 | Signal-with-start |

### ALWAYS signals Upload

Even with 0 valid videos, DownloadWorkflow signals UploadWorkflow. This ensures:
- `check_and_mark_download_complete` runs (checks if 10 downloads registered)
- The download count increments correctly for the while-loop exit condition

---

## UploadWorkflow

**Trigger**: Signal-with-start from `queue_videos_for_upload`  
**ID**: `upload-{event_id}` (deterministic — ONE per event)  
**Pattern**: Signal-based FIFO queue

### How it works

1. Workflow starts (or already running) via signal-with-start
2. `add_videos` signal handler enqueues batch into `deque`
3. Main loop: `wait_condition(pending > 0, timeout=5min)`
4. Process one batch at a time
5. Idle timeout (no signals for 5 min) → complete

### Batch processing

```
fetch_event_data → MD5 dedup → scoped perceptual dedup →
  upload new / replace inferior / bump popularity →
  save objects → recalculate ranks → cleanup → check completion
```

### Scoped deduplication (via asyncio.gather)

```python
verified_result, unverified_result = await asyncio.gather(
    deduplicate_pool(verified_videos, verified_s3_videos),
    deduplicate_pool(unverified_videos, unverified_s3_videos),
)
```

Each pool is an independent Temporal activity with `heartbeat_timeout=120s` and `start_to_close_timeout=1h`. Zero shared state → true parallelism.

### Upload outcomes (per video)

| Outcome | Action | Fields Updated |
|---------|--------|---------------|
| **New** | `upload_single_video` | s3_url, perceptual_hash, timestamp_verified, etc. |
| **Replace** | `replace_s3_video` + `update_video_in_place` | All fields of replaced video |
| **Duplicate** | `bump_video_popularity` | popularity += 1 |

### Activities

| Activity | STC / Heartbeat | Retries | Notes |
|----------|----------------|---------|-------|
| `fetch_event_data` | 30s | 3 | Returns existing S3 videos + timestamp fields |
| `deduplicate_by_md5` | 30s | 2 | Fast exact dedup |
| `deduplicate_videos` | HB 120s, STC 1h | 3 | Scoped perceptual dedup |
| `upload_single_video` | 60s | 3 | S3 PUT + metadata |
| `update_video_in_place` | 30s | 3 | Atomic replacement in MongoDB |
| `replace_s3_video` | 30s | 3 | S3 DELETE + PUT |
| `bump_video_popularity` | 15s | 2 | Increment counter |
| `save_video_objects` | 30s | 3 | Persist to MongoDB |
| `recalculate_video_ranks` | 30s | 2 | Sort: verified → popularity → file_size |
| `cleanup_individual_files` | 30s | 2 | Delete processed temp files |
| `cleanup_fixture_temp_dirs` | 60s | 2 | Full fixture cleanup on completion |
| `cleanup_upload_temp` | 30s | 2 | Single batch temp cleanup |
| `check_and_mark_download_complete` | 30s | 3 | Set `_download_complete` at count ≥ 10 |

---

## Perceptual Hash Algorithm

### Generation

1. Sample frames every **0.25 seconds** (dense sampling)
2. Apply **histogram equalization** (normalize brightness)
3. Compute **dHash** (difference hash) — 64-bit per frame
4. Format: `dense:0.25:<ts>=<hash>,<ts>=<hash>,...`

### Matching

**Offset-tolerant comparison**: Tries all possible time alignments between two videos. For each offset:
- Walk through frames at matching timestamps
- Frame match = Hamming distance ≤ 10 bits (of 64)
- Require **3 consecutive matching frames** at consistent offset

### Quality comparison

When two videos match by perceptual hash:
- **Durations within 15%** → same clip → prefer **larger file** (higher resolution)
- **Durations differ >15%** → different clips → prefer **longer duration** (more content)

---

## Retry & Timeout Strategy

### Principles

1. **Heartbeat > timeout** for long operations — proves progress, doesn't set arbitrary limits
2. **STC (start-to-close)** for bounded operations — known maximum duration
3. **Exponential backoff** for transient failures — API rate limits, network blips
4. **Low retry count** for permanent failures — don't hammer a dead service

### Heartbeat patterns

| Activity | Heartbeat Interval | What it reports |
|----------|--------------------|-----------------|
| `generate_video_hash` | Every 5 frames | Frame count processed |
| `deduplicate_videos` | 120s | Pool processing status |
| `execute_twitter_search` | 15s | Search progress |

### Timeout hierarchy

```
Workflow execution: No timeout (runs to completion)
Task timeout: 60–180s (replay + new work)
Activity STC: 10s–1h (depends on operation)
Activity heartbeat: 15s–120s (proves liveness)
```

---

## Structured Logging

All workflows use `src.utils.footy_logging` for Grafana/Loki-compatible JSON output:

```python
log.info(workflow.logger, MODULE, "action_name", "Human message",
         event_id=event_id, count=5)
```

Output: `{"level": "INFO", "module": "download_workflow", "action": "action_name", "msg": "Human message", "event_id": "...", "count": 5, "ts": "..."}`

Set `LOG_FORMAT=pretty` for development-friendly colored output.
