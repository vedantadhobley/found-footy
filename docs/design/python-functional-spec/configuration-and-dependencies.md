# Python configuration and external dependencies

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 12. Configuration Reference

All constants are in `archive/src/utils/orchestration_config.py`
unless noted.

### Monitor / Event Debounce

| Constant | Value | Purpose |
| --- | --- | --- |
| `MONITOR_DEBOUNCE_STABLE_COUNT` | 3 | Polls before event is trusted (NEW, REMOVED) |
| `MONITOR_DROP_THRESHOLD` | 3 | Polls before event is deleted (VAR) |
| `MONITOR_STAGING_LOOKAHEAD_MINUTES` | 30 | Pre-activate window |
| `MONITOR_STAGING_INTERVAL_MINUTES` | 15 | Staging polling interval |

### Twitter Discovery Loop

| Constant | Value | Purpose |
| --- | --- | --- |
| `TWITTER_MAX_ATTEMPTS` | 15 | Safety cap on search attempts |
| `TWITTER_REQUIRED_DOWNLOADS` | 10 | Target registration count to exit loop |
| `TWITTER_MAX_VIDEOS_PER_ATTEMPT` | 5 | Top N longest videos to download per attempt |
| `TWITTER_SEARCH_MAX_AGE_MINUTES` | 3 | Age filter for tweets (stop scrolling) |
| `TWITTER_ATTEMPT_SPACING_SECONDS` | 60 | START-to-START delay between attempts |
| `TWITTER_ATTEMPT_MIN_WAIT_SECONDS` | 10 | Minimum wait if attempt completes early |

### Upload Workflow

| Constant | Value | Purpose |
| --- | --- | --- |
| `UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES` | 5 | Signal queue timeout before exit |

### Phase 4 — Coverage SLO

| Constant | Value | Purpose |
| --- | --- | --- |
| `MATCH_COVERAGE_SLO_THRESHOLD` | 0.5 | Alert if coverage < 50% |
| `SLO_TRACKED_LEAGUE_IDS` | {39, 140, 78, 135, 61, 2, 3, 848} | Leagues with SLO |

### Canary (DOM Selector Validation)

| Constant | Value | Purpose |
| --- | --- | --- |
| `DOM_CANARY_QUERY` | "football goal" | Synthetic test query |
| `DOM_CANARY_MAX_AGE_MINUTES` | 1440 | 24-hour window |
| `DOM_CANARY_MIN_TWEETS` | 3 | Minimum results to pass canary |

### LLM Concurrency

| Constant | Value | Purpose |
| --- | --- | --- |
| `LLM_CONCURRENCY_PER_WORKER` | 2 | In-flight AI validation requests per worker |

### Event Configuration

| Module | Constant | Value | Purpose |
| --- | --- | --- | --- |
| `event_config.py` | `TRACKABLE_EVENT_TYPES` | ["Goal"] | Only these event types tracked |
| `event_config.py` | `TRACKABLE_GOAL_DETAILS` | ["Normal Goal", "Penalty", "Own Goal"] | Goal detail types tracked |

### Fixture Retention

| Constant | Value | Purpose |
| --- | --- | --- |
| `FIXTURE_RETENTION_DAYS` | 14 | Days to keep completed fixtures |

---

## 13. External Dependencies & Contracts

### API-Football

**Endpoints used:**
- `GET /fixtures` (batch by IDs) → `fixtures_batch(fixture_ids: List[int])`
- `GET /fixtures?date=YYYY-MM-DD` → `get_fixtures_for_date(date: str)`

**Expected fields:** Fixture, league, teams, goals, score, events
arrays with full player/assist metadata.

**Rate limiting:** Tier-dependent. No formal backoff/retry beyond
Temporal retries.

### Twitter (Browser Automation)

**Service:** Firefox Selenium-based automation running in separate
Docker container.

**Endpoints:**
- `POST /search` → `execute_twitter_search()`
- Expects: query string, existing_video_urls, max_age_minutes
- Returns: List of video metadata

**Fragility points:**
- Twitter DOM selectors can change (addressed by DOM canary test)
- Rate limiting / login required (VNC intervention)
- Browser may crash (restart handled by Docker health checks)

### Wikidata (SPARQL API)

**Used by:** RAG pipeline (get_team_aliases activity)

**Query:** SPARQL to fetch team aliases by QID.

### joi LLM (Qwen3-VL on llama.cpp)

**Endpoints:**
- `POST /predict` → `validate_video_is_soccer()`
- Input: video file path, event_minute, event_extra
- Output: JSON with is_soccer, is_screen_recording, clock_visible,
  extracted_minute

**Concurrency:** Semaphore-limited to 2 concurrent requests per
worker (`LLM_CONCURRENCY_PER_WORKER`).

### MinIO / S3

**Bucket:** footy-videos (or configured)

**Operations:**
- `PUT object` → `upload_single_video()`
- `GET object` (for metadata) → `deduplicate_videos()`

**Key format:** `{event_id}_{md5[:8]}.mp4`

**URL format:** `/video/footy-videos/{fixture_id}/{event_id}/{key}`

### MongoDB

**Collections:** fixtures_staging, fixtures_active, fixtures_live,
fixtures_completed, team_aliases, video_shares (legacy)

**Connection:** Single replica (no sharding, no HA)

**Transactions:** Not used (last-write-wins semantics)

### Temporal

**Workflow types:** IngestWorkflow, MonitorWorkflow, TwitterWorkflow,
DownloadWorkflow, UploadWorkflow

**Activity queues:** "found-footy" (single queue for all activities)

---

## 14. Retention & Cleanup Policies

### Fixtures

- Created: at ingest time
- Deleted: **14 days after completion**
- Calculation: `cleanup_old_fixtures(14)` runs at end of ingest,
  deletes fixtures with `fixture.date < today - 14 days`

### S3 Videos

- Lifetime: Tied to fixture retention
- Cleanup: When fixture is deleted, all S3 videos for that fixture
  are deleted
- Orphaned videos: If UploadWorkflow crashes after S3 PUT but before
  MongoDB write, videos become orphaned. No automatic cleanup.

### Events

- Lifetime: Tied to fixture (deleted when fixture deleted)
- Event deletion: Also happens immediately when VAR'd

### Team Aliases

- Lifetime: Session-persistent (or until manually refreshed)
- Refresh: Only when new team encountered at ingest time
- No TTL

### Twitter Cookies

- Lifetime: ~30 days (browser session)
- Persistence: Stored in Firefox profile (mounted volume)
- Refresh: Automatically refreshed by Selenium
- Expiry handling: VNC intervention if explicit re-login needed

### Event Telemetry

- Lifetime: Tied to event (deleted with fixture after 14 days)

### Monitor/Drop Workflows Array

- Lifetime: Tied to event (cleared if event reappears after VAR)
- Array growth: Unbounded per event

---
