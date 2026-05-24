# Structured Logging for Grafana/Loki

This document describes the structured JSON logging system designed for efficient Grafana Loki dashboarding.

## Overview

All logs are emitted as JSON with consistent fields that enable powerful filtering and querying in Grafana Loki without relying on fragile string matching.

## Logging Module Locations

> **Important**: Module names intentionally avoid `logging.py` to prevent shadowing Python's built-in `logging` module.

| Service | Module File | Import Path |
|---------|-------------|-------------|
| **Temporal Worker** | `src/utils/footy_logging.py` | `from src.utils.footy_logging import log, get_fallback_logger, configure_logging` |
| **Scaler Service** | `src/scaler/scaler_logging.py` | `from src.scaler.scaler_logging import ScalerLogger` |
| **Twitter Service** | `twitter/twitter_logging.py` | `from twitter.twitter_logging import log` |

### Key Differences

- **Worker logging** (`footy_logging`): Requires a logger parameter (from `activity.logger` or `workflow.logger`) for Temporal context
- **Scaler logging** (`scaler_logging`): Standalone class-based logger, no logger parameter needed
- **Twitter logging** (`twitter_logging`): Standalone module-level logger, no logger parameter needed

## Log Format

Every log entry contains these standard fields:

```json
{
  "ts": "2025-01-15T14:30:22.123456",
  "level": "INFO",
  "module": "upload",
  "action": "s3_upload_success",
  "msg": "Video uploaded to S3",
  "event_id": "goal_123_456",
  "s3_url": "s3://bucket/path/video.mp4",
  "duration_s": 2.5
}
```

### Standard Fields

| Field | Type | Description |
|-------|------|-------------|
| `ts` | ISO timestamp | When the log was emitted |
| `level` | string | `DEBUG`, `INFO`, `WARNING`, `ERROR` |
| `module` | string | Source module (e.g., `upload`, `download`, `monitor`) |
| `action` | string | Specific action being logged (e.g., `s3_upload_success`) |
| `msg` | string | Human-readable message |

### Context Fields

Additional fields are included based on the operation:

- `event_id` - The event identifier
- `fixture_id` - The fixture identifier
- `workflow_id` - Temporal workflow ID
- `team_id` - API-Football team ID
- `player_name` - Player name
- `error` - Error message (for failures)
- `traceback` - Full stack trace (for critical errors, via `traceback.format_exc()`)
- `count` - Numeric counts
- `duration_s` / `duration_ms` - Timing information

## Modules

### Temporal Worker Modules
| Module | Description |
|--------|-------------|
| `download` | Video download, validation, hash generation |
| `upload` | S3 upload, deduplication, MongoDB updates |
| `monitor` | Fixture monitoring, event processing |
| `twitter` | Twitter search activity (via session service) |
| `ingest` | Fixture ingestion, RAG pre-caching |
| `rag` | Team alias lookup (Wikidata + LLM) |
| `monitor_workflow` | Monitor orchestrator workflow |
| `twitter_workflow` | Twitter search workflow |
| `download_workflow` | Download pipeline workflow |
| `upload_workflow` | Upload queue workflow |
| `ingest_workflow` | Daily ingest workflow |
| `rag_workflow` | RAG alias resolution workflow |
| `worker` | Temporal worker startup, schedules, connections |

### Twitter Service Modules
| Module | Description |
|--------|-------------|
| `twitter_app` | FastAPI application and service registry |
| `twitter_session` | Browser automation, authentication, search |
| `twitter_auth` | Cookie management and login automation |

### Scaler Service Modules
| Module | Description |
|--------|-------------|
| `scaler` | Auto-scaling workers/Twitter based on queue depth |
| `registry` | Twitter instance service discovery and load balancing |

### Data Layer Modules
| Module | Description |
|--------|-------------|
| `mongo_store` | MongoDB operations (fixtures, events, videos, aliases) |
| `s3_store` | S3/MinIO operations (video upload, deletion, tagging) |

### Utility Modules (Infrastructure)
| Module | Description |
|--------|-------------|
| `api_client` | API-Football REST client (leagues, teams, fixtures) |
| `team_data` | Top-flight team management and caching |

## Action Naming Convention

Actions follow a consistent naming pattern:

- `*_start` - Operation beginning
- `*_success` - Successful completion
- `*_failed` - Failure with error
- `*_complete` - Final completion (may include partial success)
- `*_skipped` - Operation intentionally skipped

## Grafana Loki Queries

> **See [Loki Query Reference for Grafana](#loki-query-reference-for-grafana)** below for the full
> query reference. Promtail indexes `module`, `action`, and `level` as native Loki labels,
> so queries use label selectors like `{module="download", level="ERROR"}` — no `| json |`
> parsing needed for filtering.

## Usage in Code

### Activities (Temporal Worker)

```python
from temporalio import activity
from src.utils.footy_logging import log

MODULE = "upload"

@activity.defn
async def upload_video(file_path: str, event_id: str):
    log.info(activity.logger, MODULE, "upload_start",
             "Starting upload", file_path=file_path, event_id=event_id)
    
    try:
        # ... upload logic ...
        log.info(activity.logger, MODULE, "upload_success",
                 "Upload complete", s3_url=url, event_id=event_id)
    except Exception as e:
        log.error(activity.logger, MODULE, "upload_failed",
                  "Upload failed", error=str(e), event_id=event_id)
        raise
```

### Workflows (Temporal Worker)

```python
from temporalio import workflow
from src.utils.footy_logging import log

MODULE = "upload_workflow"

@workflow.defn
class UploadWorkflow:
    @workflow.run
    async def run(self, input):
        log.info(workflow.logger, MODULE, "workflow_started",
                 "UploadWorkflow started", event_id=input.event_id)
        
        # ... workflow logic ...
        
        log.info(workflow.logger, MODULE, "workflow_complete",
                 "UploadWorkflow complete", videos_uploaded=count)
```

### Twitter Service (Standalone Container)

The Twitter service has its own logging module with a simpler interface (no logger parameter needed):

```python
from twitter.twitter_logging import log

MODULE = "twitter_session"

def search_videos(query: str):
    log.info(MODULE, "search_start", "Searching", query=query)
    
    try:
        # ... search logic ...
        log.info(MODULE, "search_complete", "Search complete", videos_found=len(videos))
    except Exception as e:
        log.error(MODULE, "search_error", "Search error", error=str(e))
        raise
```

### Scaler Service (Standalone Container)

The scaler service has its own logging module, similar to Twitter:

```python
from src.scaler.scaler_logging import ScalerLogger

MODULE = "scaler"
log = ScalerLogger()

def scale_workers(count: int):
    log.info(MODULE, "scaling_workers", f"Scaling workers to {count}", count=count)
    
    try:
        # ... scaling logic ...
        log.info(MODULE, "scaled", "Workers scaled", workers=count)
    except Exception as e:
        log.error(MODULE, "scale_failed", "Scaling failed", error=str(e))
        raise
```

### Infrastructure Code (MongoDB, S3)

For infrastructure code that doesn't have access to activity/workflow loggers, use the fallback logger pattern:

```python
from src.utils.footy_logging import get_fallback_logger
from src.utils import footy_logging as log

MODULE = "mongo_store"

def _log_info(action: str, msg: str, **kwargs):
    """Log info using fallback logger."""
    log.info(get_fallback_logger(), MODULE, action, msg, **kwargs)

def _log_error(action: str, msg: str, **kwargs):
    """Log error using fallback logger."""
    log.error(get_fallback_logger(), MODULE, action, msg, 
              error=kwargs.get('error', ''), **{k: v for k, v in kwargs.items() if k != 'error'})

# Usage in methods
def store_fixture(self, fixture_id: int, data: dict):
    _log_info("fixture_stored", f"Stored fixture {fixture_id}", fixture_id=fixture_id)
```
```

## Pretty Print Mode

For local development, set `LOG_FORMAT=pretty` to get human-readable logs:

```
2025-01-15 14:30:22.123 | INFO | upload | s3_upload_success | Video uploaded to S3 | event_id=goal_123 s3_url=s3://...
```

In production, logs are JSON for Loki ingestion.

## Key Actions by Module

### download (Video Download Activity)
**Cookies:**
- `cookies_loaded` - Twitter cookies loaded from file (count, auth_cookies)
- `cookies_missing` - No cookies file found
- `cookies_load_failed` - Failed to load cookies (error)

**Workflow Tracking:**
- `workflow_registered` - Download workflow registered in MongoDB
- `workflow_register_failed` - Failed to register workflow (error)
- `completion_check` - Checking download completion status
- `completion_check_failed` - Failed to check completion (error)

**Download Process:**
- `download_started` - Starting video download (video_idx, video_url, tweet_url)
- `tweet_id_extraction_failed` - Could not extract tweet ID from URL
- `syndication_api_error` - Twitter syndication API error (status_code)
- `syndication_timeout` - Syndication API timed out
- `syndication_error` - General syndication error
- `filtered_resolution` - Video rejected: low resolution (short_edge, min_edge, width, height)
- `filtered_aspect` - Video rejected: portrait/square (aspect_ratio, min_ratio, width, height)
- `filtered_duration` - Video rejected: too short/long (duration_s, min/max)
- `no_variants` - No video variants found in response
- `variant_selected` - Best video variant selected (bitrate, url)
- `cdn_auth_failed` - CDN authentication failed (status_code)
- `cdn_error` - CDN download error (status_code)
- `cdn_timeout` - CDN request timed out
- `cdn_download_error` - CDN download failed (error)
- `file_missing` - Downloaded file doesn't exist
- `file_too_small` - Downloaded file too small (size)
- `downloaded` - Video downloaded successfully (file_path, size)

**Validation:**
- `validate_started` - Starting video validation
- `validate_file_missing` - File to validate doesn't exist
- `frame_extraction_failed` - Could not extract frames for validation
- `frame_extraction_timeout` - Frame extraction timed out
- `frame_extraction_error` - Frame extraction error
- `vision_call` - Calling vision API (frame_idx)
- `vision_check` - Vision API response (frame_idx, has_field, confidence)
- `vision_tiebreaker` - Multiple frames checked for validation
- `vision_http_error` - Vision API HTTP error (status_code)
- `vision_connect_failed` - Vision API unreachable (error)
- `vision_timeout` - Vision API timed out
- `vision_error` - Vision API error (error)
- `validate_rejected` - Video failed validation (reason: no_field/not_soccer)
- `validate_passed` - Video passed validation

**Hash Generation:**
- `hash_started` - Starting perceptual hash generation
- `hash_file_missing` - File to hash doesn't exist
- `hash_generated` - Hash generated successfully (hash, frame_count, duration)
- `hash_low_frames` - Generated hash has few frames (frame_count)
- `hash_no_frames` - No frames extracted for hashing
- `hash_invalid_format` - Invalid hash format generated
- `hash_frame_failed` - Single frame hash failed (position)

**Finalization:**
- `video_ready` - Video ready for upload (file_path, hash, duration, resolution)
- `metadata_failed` - Could not extract video metadata (error)
- `temp_cleaned` - Temporary files cleaned up

### upload (Video Upload Activity)
**Queue Management:**
- `queue_started` - Queuing videos for upload (event_id, count)
- `queue_success` - Videos queued successfully
- `queue_failed` - Failed to queue videos (error)

**Event Data:**
- `fetch_event_data` - Fetching event data from MongoDB
- `fixture_not_found` - Fixture not in database
- `event_not_found` - Event not in fixture
- `no_discovered_videos` - No discovered videos for event
- `existing_video` - Found existing video in event
- `skip_already_downloaded` - Skipping already downloaded URL
- `no_new_videos` - No new videos to process
- `existing_for_dedup` - Existing S3 videos for deduplication (count)
- `videos_found` - Videos ready to process (new, downloaded_urls, existing_s3)

**Deduplication:**
- `dedup_started` - Starting deduplication (successful_downloads, existing_s3)
- `dedup_prefiltered` - Videos filtered by duration
- `dedup_no_downloads` - No downloads to deduplicate
- `dedup_missing_hash` - Videos without hash cannot be deduplicated
- `dedup_clustered` - Videos grouped into clusters (clusters, total)
- `cluster_winner_resolution` - Cluster winner by resolution (winner_file, resolution_score)
- `cluster_winner_length` - Cluster winner by length (winner_file, duration)
- `batch_dedup_complete` - Batch dedup finished (input, keepers, removed)
- `dedup_bypassed` - Video has no hash, deduplication skipped
- `new_video` - No S3 match found - video is new
- `dedup_complete` - Deduplication finished (to_upload, replacements, popularity_bumps)

**File Cleanup:**
- `delete_file_failed` - Failed to delete duplicate file (file_path, error)
- `batch_delete_failures` - Multiple file deletions failed (failed, total)
- `local_file_deleted` - Local file deleted after S3 match
- `cleanup_file_failed` - Failed to remove discarded file
- `cleanup_failures` - Multiple cleanup deletions failed (failed, total)

**Upload & Replace:**
- `replace_started` - Starting atomic in-place update
- `replace_complete` - In-place update succeeded (s3_url, new_popularity)
- `replace_no_modification` - No modification made during replace
- `replace_failed` - In-place update failed (error)
- `replace_not_found` - Video not found for in-place update

**MongoDB Operations:**
- `mongo_remove_success` - Video removed from MongoDB
- `mongo_remove_not_found` - Video not found in MongoDB
- `mongo_remove_failed` - MongoDB update failed (error)
- `popularity_bump_started` - Starting popularity bump
- `popularity_bump_success` - Popularity updated (new_popularity)
- `popularity_bump_failed` - Popularity update failed
- `save_videos_started` - Saving video objects (count)
- `save_videos_failed` - Failed to save video objects (error)
- `recalc_ranks` - Recalculating video ranks

**Cleanup:**
- `file_delete_failed` - Failed to delete file (file_path, error)
- `files_deleted` - Deleted individual files (count)
- `temp_dir_deleted` - Deleted temp directory (path)
- `temp_dir_delete_failed` - Failed to delete temp dir (error)
- `fixture_cleanup_complete` - Fixture cleanup finished (deleted)
- `upload_temp_cleaned` - Upload temp dir cleaned

### twitter (Twitter Search Activity)
**Workflow Tracking:**
- `set_monitor_complete` - Monitor complete flag set (event_id, success)
- `set_monitor_complete_failed` - Failed to set monitor complete (error)
- `get_download_count` - Download workflow count retrieved (event_id, count)
- `get_download_count_failed` - Failed to get count (error)

**Event Validation:**
- `check_event_exists` - Checking if event exists (fixture_id, event_id)
- `event_exists` - Event confirmed to exist
- `event_not_found` - Event not found in fixture
- `fixture_not_found` - Fixture not found in active

**Search Data:**
- `get_search_data` - Getting Twitter search data (fixture_id, event_id)
- `no_twitter_search` - No twitter_search field on event
- `search_data_ready` - Search data prepared (query, existing_urls)

**Instance Discovery:**
- `instance_unhealthy` - Twitter instance returned non-200 (url, status)
- `instance_unreachable` - Twitter instance unreachable (url, error)
- `instances_unavailable` - Multiple instances unavailable (healthy, unhealthy)
- `no_healthy_instances` - No healthy instances found (fallback)
- `instance_pool_changed` - Instance pool changed (instance_count)

**Search Execution:**
- `execute_search_started` - Starting Twitter search (query, max_age_minutes, excluding, instance, healthy)
- `heartbeat` - Search heartbeat (count, query)
- `search_post` - Sending search request (url)
- `service_unreachable` - Twitter service unreachable (url, error)
- `search_timeout` - Search timed out after 120s (query)
- `auth_failed` - Twitter authentication required
- `service_error` - Twitter service error (status_code, response)
- `search_success` - Search completed (query, found)

**Save Results:**
- `save_videos_started` - Saving discovered videos (fixture_id, event_id, count)
- `no_videos_to_save` - No videos to save
- `save_videos_failed` - Failed to save videos (error)
- `save_videos_success` - Videos saved (event_id, count)
- `save_videos_error` - Save error (error)

### monitor (Fixture Monitoring Activity)
**Staging:**
- `staging_empty` - No staging fixtures to fetch
- `staging_poll` - Polling staging fixtures for updates
- `staging_skip` - Staging poll skipped (no staging fixtures or not needed)
- `staging_fetch_started` - Fetching staging fixtures (count)
- `staging_fetch_success` - Retrieved staging data (count)
- `staging_fetch_failed` - Staging fetch failed (error)
- `staging_complete` - Staging processing complete

**Pre-activation:**
- `pre_activate_started` - Pre-activation check started (staging_count, lookahead_minutes)
- `pre_activate_polling` - Polling fixtures not in current interval (to_poll)
- `pre_activate_found` - Found fixtures to pre-activate (count)
- `pre_activate_success` - Pre-activated fixture (fixture_id)
- `pre_activate_complete` - Pre-activation complete (polled, pre_activated)

**Active Monitoring:**
- `active_fetch_started` - Fetching active fixtures (count)
- `active_fetch_success` - Retrieved active data (count)
- `active_fetch_failed` - Active fetch failed (error)

**Event Processing:**
- `process_events_started` - Processing fixture events (fixture_id, workflow_id)
- `new_event` - New event detected (event_id, type, player)
- `event_debounced` - Event confirmed after debounce (event_id, workflow_count)
- `trigger_twitter` - Triggering Twitter workflow (event_id)
- `var_detected` - VAR removal detected (event_id)
- `events_not_ready` - Events not ready for completion (monitored, download, total)

**Completion:**
- `completion_started` - Completion counter started (fixture_id, winner)
- `completion_check` - Completion check (fixture_id, count, max_count, winner)
- `completion_waiting` - Waiting for completion (fixture_id, count, max_count)
- `fixture_completed` - Fixture moved to completed (fixture_id)
- `completion_error` - Completion error (fixture_id, error)

### ingest (Fixture Ingestion Activity)
**Fetch:**
- `fetch_started` - Fetching fixtures from API (date, leagues)
- `fetch_success` - Fixtures retrieved (count)
- `fetch_failed` - Fetch failed (error)
- `batch_fetch_started` - Batch fetching by IDs (count)
- `batch_fetch_success` - Batch fetch complete (count)

**Categorization:**
- `categorize_started` - Categorizing fixtures
- `categorize_complete` - Categorization complete (staging, live, active, skipped)

**Cleanup:**
- `cleanup_started` - Starting old fixture cleanup (retention_days)
- `cleanup_fixture` - Cleaning up fixture (fixture_id, age_days)
- `cleanup_s3_deleted` - S3 videos deleted (fixture_id, count)
- `s3_delete_failed` - S3 delete failed (fixture_id, error)
- `orphan_s3_delete_failed` - Orphan S3 delete failed (key, error)
- `cleanup_complete` - Cleanup complete (fixtures_deleted, videos_deleted, failed_s3)

### rag (Team Alias RAG Activity)
**Cache:**
- `cache_hit` - Aliases found in cache (aliases)
- `cache_miss` - Cache miss, starting RAG pipeline

**API Lookup:**
- `api_team_info` - API team info retrieved (team_name, national, country, city)
- `api_lookup_failed` - API lookup failed (team_id)
- `get_aliases_started` - Starting alias lookup (team_id, team_name, team_type)

**Wikidata:**
- `city_match` - City match found in Wikidata (qid, label, description)
- `country_match` - Country match found (qid, label, description)
- `better_candidate` - Better Wikidata candidate found (qid, label, desc_quality)
- `best_candidate` - Best candidate selected (qid, desc_quality, search_terms_tried)
- `no_wikidata_match` - No Wikidata match after all searches (team_name, search_terms_tried)
- `wikidata_qid_found` - Found Wikidata QID (qid)
- `wikidata_aliases` - Wikidata aliases retrieved (count, sample)
- `no_wikidata_qid` - No Wikidata QID found (team_name)
- `search_error` - Wikidata search error (search_term, error)
- `wikidata_fetch_error` - Wikidata fetch error (error)

**LLM:**
- `cleaned_aliases` - Cleaned word list (count, sample)
- `llm_request` - Sending to LLM (prompt_preview)
- `llm_response` - LLM response received (response)
- `hallucination_rejected` - Rejected hallucinated word (word)
- `hallucinations_filtered` - Summary of filtered hallucinations (rejected, accepted)
- `final_aliases` - Final aliases selected (count, aliases)
- `llm_invalid` - LLM returned invalid/empty (response)
- `llm_unavailable` - LLM server not available (llm_url)
- `llm_error` - LLM error (error)
- `fallback_aliases` - Using fallback aliases (count, aliases)

**Save:**
- `save_aliases_started` - Saving aliases (event_id, aliases)
- `save_aliases_success` - Aliases saved
- `save_aliases_failed` - Save failed (error)

### worker (Temporal Worker)
**Startup:**
- `connecting` - Connecting to Temporal (temporal_host)
- `connected` - Connected to Temporal server (temporal_host)

**Schedules:**
- `setup_schedules` - Setting up workflow schedules
- `schedule_created` - Created new schedule (schedule_id)
- `schedule_updated` - Updated existing schedule (schedule_id)

**Worker State:**
- `worker_started` - Worker started (task_queue)
- `workflows_registered` - Workflows registered (workflow_count)
- `activities_registered` - Activities registered (activity_count)
- `schedules_configured` - Schedules configured
- `worker_stopped` - Worker stopped gracefully
- `worker_failed` - Worker failed (error)

### scaler (Auto-Scaling Service)
**Startup:**
- `connecting_temporal` - Connecting to Temporal (host)
- `connected_temporal` - Connected to Temporal
- `init_docker` - Initializing Docker client (compose_file)
- `docker_ready` - Docker client ready
- `startup` - Scaler service started (task_queue, min_instances, max_instances, thresholds)
- `ensuring_min_instances` - Ensuring minimum instances
- `monitoring_started` - Starting monitoring loop

**Metrics:**
- `task_queue_error` - Error getting task queue metrics (error)
- `workflow_count_error` - Error counting workflows (error)
- `active_goals_error` - Error getting active goals (error)
- `goals_summary_error` - Error getting goals summary (error)

**State:**
- `heartbeat` - Regular status update (active_workflows, workers_running, twitter_running, active_goals, goals metrics)
- `state_changed` - State changed from previous (same fields as heartbeat)

**Scaling:**
- `container_list_error` - Error listing containers (service, error)
- `scaling` - Scaling service (service, current, target)
- `scaled` - Scaled service successfully (service, target, actual)
- `scale_mismatch` - Scale target not reached (service, target, actual)
- `scale_failed` - Failed to scale service (service, error)
- `scaling_workers` - Scaling workers (current, target, active_workflows)
- `scaling_twitter` - Scaling Twitter (current, target, active_goals)
- `worker_cooldown` - Worker scaling in cooldown (remaining_seconds)
- `twitter_cooldown` - Twitter scaling in cooldown (remaining_seconds)

**Twitter Discovery:**
- `twitter_discovery` - Twitter instance discovery (healthy, unhealthy)
- `no_healthy_twitter` - No healthy Twitter instances (fallback_count)

### registry (Twitter Instance Registry)
**Registration:**
- `instance_registered` - Twitter instance registered (instance_id, url)
- `register_failed` - Failed to register instance (instance_id, error)
- `instance_unregistered` - Twitter instance unregistered (instance_id)
- `unregister_failed` - Failed to unregister instance (instance_id, error)

**Health:**
- `heartbeat_failed` - Heartbeat failed for instance (instance_id, error)
- `get_instances_failed` - Failed to get Twitter instances (error)
- `no_instances_available` - No Twitter instances available (default_url)
- `instance_cache_refreshed` - Instance cache refreshed (previous_count, new_count)

### mongo_store (MongoDB Data Layer)
**Staging:**
- `staging_stored` - Fixture stored in staging (fixture_id)
- `staging_found` - Fixture found in staging (fixture_id)
- `staging_not_found` - Fixture not in staging (fixture_id)
- `staging_deleted` - Fixture deleted from staging (fixture_id)
- `staging_fixtures_error` - Error getting staging fixtures (error)

**Live:**
- `live_fixture_stored` - Fixture stored in live (fixture_id)
- `live_fixture_found` - Fixture found in live (fixture_id)
- `live_fixture_not_found` - Fixture not in live (fixture_id)
- `get_live_error` - Error getting live fixture (error)
- `live_fixtures_error` - Error getting live fixtures (error)

**Active:**
- `active_fixture_added` - Fixture added to active (fixture_id)
- `active_fixture_found` - Fixture found in active (fixture_id)
- `active_fixture_not_found` - Fixture not in active (fixture_id)
- `active_count_fetched` - Active fixture count (count)
- `get_active_error` - Error getting active fixture (error)
- `active_fixtures_error` - Error getting active fixtures (error)

**Events:**
- `event_added` - Event added to fixture (fixture_id, event_id)
- `event_update_failed` - Failed to update event (error)
- `videos_added` - Videos added to event (event_id, count)
- `add_videos_duplicate` - Duplicate video URLs skipped (duplicate_count, added_count)
- `ranks_recalculated` - Video ranks recalculated (event_id, video_count)
- `popularity_updated` - Video popularity updated (video_s3_url, new_popularity)
- `video_not_found_popularity` - Video not found for popularity update (s3_url)
- `update_popularity_error` - Error updating popularity (error)

**Workflow Tracking:**
- `workflow_registered` - Monitor workflow registered (fixture_id, event_id, workflow_id)
- `register_workflow_error` - Error registering workflow (error)
- `workflows_dropped` - Dropped workflows removed (fixture_id, event_id, workflow_id)
- `drop_workflow_error` - Error dropping workflow (error)
- `download_workflow_registered` - Download workflow registered (event_id, count)
- `download_register_error` - Error registering download (error)
- `download_count_fetched` - Download count fetched (event_id, count)
- `download_count_error` - Error getting download count (error)

**Completion:**
- `monitor_marked_complete` - Monitor marked complete (fixture_id, event_id)
- `monitor_mark_error` - Error marking monitor complete (error)
- `download_marked_complete` - Download marked complete (event_id)
- `download_check_error` - Error checking download complete (error)
- `increment_completion_error` - Error incrementing completion count (fixture_id, error)
- `completion_ready_check_error` - Error checking completion ready (fixture_id, error)
- `fixture_not_found_complete` - Fixture not found for completion (fixture_id)
- `complete_fixture_error` - Error completing fixture (fixture_id, error)
- `get_completed_fixtures_error` - Error getting completed fixtures (error)

**VAR Removal:**
- `unexpected_s3_url` - Unexpected S3 URL format (s3_url)
- `var_video_deleted` - Deleted S3 video for VAR'd goal (key)
- `s3_delete_failed` - Failed to delete S3 video (s3_url, error)
- `var_s3_delete_summary` - VAR S3 cleanup summary (event_id, deleted, failed)
- `var_event_removed` - Removed VAR'd event from MongoDB (event_id)
- `remove_event_error` - Error removing event (event_id, error)

**Sync:**
- `fixture_synced` - Fixture data synced (fixture_id)
- `sync_failed` - Sync failed (fixture_id, error)

**Team Aliases:**
- `get_team_alias_error` - Error getting team alias (team_id, error)
- `upsert_team_alias_error` - Error upserting team alias (team_id, error)
- `get_all_team_aliases_error` - Error getting all aliases (error)
- `delete_team_alias_error` - Error deleting alias (team_id, error)
- `clear_team_aliases_error` - Error clearing aliases (error)

**Top-Flight Cache:**
- `get_top_flight_cache_error` - Error getting top-flight cache (error)
- `save_top_flight_cache_error` - Error saving top-flight cache (error)
- `clear_top_flight_cache_error` - Error clearing top-flight cache (error)

### api_client (API-Football Client)
**Leagues & Seasons:**
- `league_not_found` - No league found for ID (league_id)
- `season_fallback` - No current season marked, using latest (league_id, season)
- `season_fetch_failed` - Failed to get current season (league_id, error)

**Teams:**
- `teams_fetched` - Fetched teams for league (league_id, season, count)
- `teams_fetch_failed` - Failed to get teams for league (league_id, season, error)
- `team_info_failed` - Failed to fetch team info (team_id, error)

**Fixtures:**
- `api_errors` - API-Football returned errors in response (errors)

### team_data (Top-Flight Team Management)
**Cache:**
- `cache_hit` - Using cached top-flight teams (count, cache_age_hours)
- `cache_saved` - Cached top-flight teams (count, season)

**Fetch:**
- `fetch_started` - Fetching top-flight teams from API-Football
- `season_determined` - Current season determined (season)
- `season_unknown` - Could not determine current season (league_name)
- `league_teams_fetched` - Fetched teams for league (league_name, count)
- `league_no_teams` - No teams found for league (league_name)

**Fallback:**
- `fallback_used` - API fetch failed, using legacy TOP_UEFA_IDS (fallback_count)

### s3_store (S3/MinIO Data Layer)
**Bucket Management:**
- `bucket_exists` - S3 bucket exists (bucket)
- `bucket_creating` - Creating S3 bucket (bucket)
- `bucket_created` - Created S3 bucket (bucket)
- `bucket_exists_race` - Bucket created by another process (bucket)
- `bucket_check_failed` - Error checking bucket (bucket, error)
- `bucket_create_failed` - Failed to create bucket (bucket, error)
- `no_credentials` - S3 credentials not configured (error)
- `unexpected_s3_error` - Unexpected S3 error (error)

**Upload:**
- `bucket_disappeared` - Bucket disappeared, recreating (bucket)
- `video_uploaded` - Video uploaded (s3_key)
- `upload_failed` - Upload failed (s3_key, error)
- `upload_video_file_failed` - Upload video file failed (goal_id, error)

**Query:**
- `list_goal_videos_error` - Error listing goal videos (goal_id, error)
- `get_hashes_error` - Error getting S3 hashes (fixture_id, event_id, error)
- `get_tags_error` - Error getting tags (s3_key, error)
- `list_videos_by_fixture_error` - Error listing fixture videos (fixture_id, error)

**Tagging:**
- `fixture_tagged` - Tagged videos in fixture (fixture_id, video_count)
- `tag_fixture_error` - Error tagging fixture (fixture_id, error)

**Delete:**
- `video_deleted` - Video deleted (s3_key)
- `delete_failed` - Delete failed (s3_key, error)

### twitter_app (Twitter FastAPI Service)
- `startup` - FastAPI startup (instance_id, port)
- `service_ready` - Service ready (authenticated)
- `registry_registered` - Registered with scaler registry
- `registry_failed` - Failed to register with registry (error)
- `fastapi_started` - FastAPI application started

### twitter_session (Twitter Browser Automation)
**Startup:**
- `startup` - Session startup
- `service_ready` - Service ready for requests
- `manual_login_required` - Manual login required

**Browser:**
- `browser_created` - Browser instance created
- `browser_setup_failed` - Browser setup failed (error)
- `browser_session_died` - Browser session died

**Cookies:**
- `cookie_restore_success` - Cookies restored successfully
- `cookie_restore_failed` - Cookie restore failed (error)
- `cookies_backed_up` - Cookies backed up
- `cookies_added` - Cookies added to browser

**Search:**
- `search_start` - Search starting (query)
- `search_complete` - Search complete (query, videos_found)
- `search_error` - Search error (error)
- `video_found` - Video found in tweet (tweet_url)
- `old_tweet_found` - Tweet too old, stopping scroll

**Download:**
- `browser_download_start` - Starting browser download
- `download_success` - Download successful
- `download_error` - Download error (error)

**Session:**
- `session_valid` - Session is valid
- `session_invalid` - Session is invalid
- `ensuring_auth` - Ensuring authentication
- `auth_cookie_restore` - Attempting cookie restore
- `auth_firefox_profile` - Using Firefox profile
- `auth_failed` - Authentication failed

### twitter_auth (Twitter Cookie/Login Management)
**Cookies:**
- `cookies_loaded` - Cookies loaded from file (count)
- `cookies_saved` - Cookies saved to file
- `cookies_load_failed` - Failed to load cookies (error)
- `cookies_save_failed` - Failed to save cookies (error)
- `cookies_valid` - Cookies are valid
- `cookies_invalid` - Cookies are invalid
- `cookies_expired` - Cookies have expired

**Login:**
- `automated_login_start` - Starting automated login
- `automated_login_success` - Automated login successful
- `automated_login_failed` - Automated login failed (error)
- `interactive_login_start` - Starting interactive login
- `login_detected` - Login detected
- `login_timeout` - Login timed out
- `email_verification_required` - Email verification required
- `no_email_verification` - No email verification needed
---

## Loki Query Reference for Grafana

> **Note**: Promtail indexes `module`, `action`, and `level` as native Loki labels for found-footy containers.
> This means you can filter by these in the label selector `{}` without needing `| json |` parsing,
> which is significantly faster. Use `| json` only when you need to access numeric or context fields.

### Service Filtering

```logql
# Temporal Worker logs
{container=~"found-footy-prod-worker-.*"}

# Twitter Service logs
{container=~"found-footy-prod-twitter-.*"}

# Scaler Service logs
{container=~"found-footy-prod-scaler"}
```

### Module-Level Queries (Label-Based)

```logql
# All download activity logs (uses indexed label - fast)
{module="download"}

# All upload activity logs
{module="upload"}

# All Twitter activity logs
{module="twitter"}

# All monitor activity logs
{module="monitor"}

# All RAG activity logs
{module="rag"}

# All ingest activity logs
{module="ingest"}

# MongoDB data layer logs
{module="mongo_store"}

# S3 data layer logs
{module="s3_store"}

# Scaler logs
{module="scaler"}
```

### Error Monitoring

```logql
# All errors across all found-footy services
{container=~"found-footy-.*", level="ERROR"}

# All warnings
{container=~"found-footy-.*", level="WARNING"}

# Download failures
{module="download", action=~".*failed.*|.*error.*"}

# Upload failures
{module="upload", action=~".*failed.*|.*error.*"}

# Twitter search failures
{module="twitter", action=~"auth_failed|search_timeout|service_error|instances_unavailable"}

# MongoDB operation failures
{module="mongo_store", level="ERROR"}

# S3 operation failures
{module="s3_store", level="ERROR"}

# Errors by module (for dashboards)
sum by (module) (count_over_time({container=~"found-footy-.*", level="ERROR"} [$__interval]))
```

### Video Processing Pipeline

```logql
# Video downloads started
{module="download", action="download_started"}

# Videos successfully downloaded
{module="download", action="downloaded"}

# Videos filtered by resolution
{module="download", action="filtered_resolution"}

# Videos filtered by aspect ratio
{module="download", action="filtered_aspect"}

# Videos filtered by duration
{module="download", action="filtered_duration"}

# Validation passes/failures
{module="download", action=~"validate_passed|validate_rejected"}

# Hash generation
{module="download", action="hash_generated"}

# S3 uploads
{module="s3_store", action="video_uploaded"}
```

### Deduplication Monitoring

```logql
# Deduplication started
{module="upload", action="dedup_started"}

# Deduplication clusters formed
{module="upload", action="dedup_clustered"}

# Batch dedup completion with stats
{module="upload", action="batch_dedup_complete"}

# Dedup complete with summary
{module="upload", action="dedup_complete"}

# New unique videos
{module="upload", action="new_video"}
```

### Twitter Search Monitoring

```logql
# Twitter searches started
{module="twitter", action="execute_search_started"}

# Twitter searches completed
{module="twitter", action="search_success"}

# Twitter instance health issues
{module="twitter", action=~"instance_unhealthy|instance_unreachable|no_healthy_instances"}

# Twitter service logs (browser automation)
{module="twitter_session"}

# Videos found by Twitter search
{module="twitter_session", action="video_found"}
```

### Fixture Lifecycle

```logql
# Staging fixtures fetched
{module="monitor", action="staging_fetch_success"}

# Staging polling
{module="monitor", action="staging_poll"}

# Staging skipped
{module="monitor", action="staging_skip"}

# Fixtures pre-activated
{module="monitor", action="pre_activate_success"}

# New events detected
{module="monitor", action="new_event"}

# Events confirmed after debounce
{module="monitor", action="event_debounced"}

# VAR removals detected
{module="monitor", action="var_detected"}

# Fixtures completed
{module="monitor", action="fixture_completed"}
```

### Scaler Metrics

```logql
# Scaler heartbeats (regular status with all numeric fields)
{module="scaler", action="heartbeat"}

# State changes (scaling decisions)
{module="scaler", action="state_changed"}

# Scaling events
{module="scaler", action=~"scaling|scaled|scaling_workers|scaling_twitter"}

# Scale mismatches (target not reached)
{module="scaler", action="scale_mismatch"}

# Extract numeric metrics from heartbeat (for stat panels / time series)
# ⚠️ IMPORTANT: Always filter action="heartbeat" when using unwrap.
# {module="scaler"} alone matches TWO streams (heartbeat + state_changed),
# both containing the same numeric fields. Without the action filter,
# unwrap returns 2 series and stat panels render both values overlapping.
# Wrap with max() for single-value panels.
max(last_over_time({module="scaler", action="heartbeat"} | json | unwrap active_workflows [$__range]))
max(last_over_time({module="scaler", action="heartbeat"} | json | unwrap goals_in_progress [$__range]))
max(last_over_time({module="scaler", action="heartbeat"} | json | unwrap goals_total [$__range]))
max(last_over_time({module="scaler", action="heartbeat"} | json | unwrap videos_total [$__range]))
```

### RAG Pipeline

```logql
# Cache hits (fast path)
{module="rag", action="cache_hit"}

# Cache misses (RAG triggered)
{module="rag", action="cache_miss"}

# Wikidata best candidates found
{module="rag", action="best_candidate"}

# No Wikidata matches
{module="rag", action="no_wikidata_match"}

# Hallucinations filtered
{module="rag", action="hallucinations_filtered"}

# Final aliases generated
{module="rag", action="final_aliases"}

# LLM unavailable (using fallback)
{module="rag", action="llm_unavailable"}
```

### Worker Lifecycle

```logql
# Worker startup
{module="worker", action="worker_started"}

# Schedule management
{module="worker", action=~"schedule_created|schedule_updated"}

# Monitor workflow cycles
{module="monitor_workflow", action="cycle_complete"}

# Worker failures
{module="worker", action="worker_failed"}
```

### Aggregation Queries for Dashboards

```logql
# Count downloads per interval (for time series panels)
sum(count_over_time({module="download", action="downloaded"} [$__interval]))

# Video processing stages funnel
sum by (action) (count_over_time({module="download", action=~"download_started|downloaded|validate_passed|hash_generated"} [$__interval]))

# Twitter search success rate
sum(count_over_time({module="twitter", action="search_success"} [$__interval]))
  /
sum(count_over_time({module="twitter", action="execute_search_started"} [$__interval]))

# Deduplication efficiency
avg_over_time({module="upload", action="batch_dedup_complete"} | json | unwrap removed [1h])
```

### Grafana Dashboard — Error Severity

The Grafana dashboard splits errors into two categories because **not all errors are equal**:
video download failures are expected (CDN blocks, rate limits, bad resolution), but infrastructure
failures (LLM down, MongoDB unreachable, S3 broken) require immediate attention.

#### 🔥 Critical Infrastructure Errors

These modules represent core infrastructure — ANY error is a red alert:

| Module | What it means |
|--------|---------------|
| `rag` | LLM / Wikidata RAG pipeline broken |
| `mongo_store` | MongoDB operations failing |
| `s3_store` | S3/MinIO storage broken |
| `scaler` | Auto-scaler can't manage workers |
| `worker` | Temporal worker crashed |
| `registry` | Twitter instance registry broken |
| `ingest` | Fixture ingestion pipeline failing |
| `api_client` | API-Football client errors |

Plus these specific actions from the `download` module (vision API failures):
- `vision_connect_failed` — Vision API unreachable
- `vision_error` — Vision API returned an error
- `vision_timeout` — Vision API timed out

```logql
# Critical infrastructure errors (for stat panels and alerting)
count_over_time({container=~"found-footy-.*", level="ERROR", module=~"rag|mongo_store|s3_store|scaler|worker|registry|ingest|api_client"} [$__range])

# Critical vision errors from download module
count_over_time({container=~"found-footy-.*", level="ERROR", module="download", action=~"vision_connect_failed|vision_error|vision_timeout"} [$__range])

# Critical failures timeline — sum by module/action for individual failure identification
sum by (module, action) (count_over_time({container=~"found-footy-.*", level="ERROR", module=~"rag|mongo_store|s3_store|scaler|worker|registry|ingest|api_client"} [$__interval]))
```

#### 📡 Expected Pipeline Noise

These are normal operational errors that don't need immediate attention:

- **Download module** (excluding vision actions) — CDN blocks, rate limits, resolution/aspect/duration filters
- **Twitter modules** (`twitter`, `twitter_session`, `twitter_auth`) — auth failures, timeouts, instance health

```logql
# Download errors (expected noise, excluding vision failures)
count_over_time({container=~"found-footy-.*", level="ERROR", module="download", action!~"vision_connect_failed|vision_error|vision_timeout"} [$__range])

# Twitter errors (expected noise)
count_over_time({container=~"found-footy-.*", level="ERROR", module=~"twitter|twitter_session|twitter_auth"} [$__range])
```

> **Dashboard layout**: Critical errors are shown as always-visible stat panels that turn red on ANY error.
> Expected noise is in a **collapsed row** that can be expanded when needed.

### Specific Field Extraction

```logql
# Extract fixture_id from logs (requires json parsing for context fields)
{module="monitor"} | json | fixture_id!="" | line_format "{{.fixture_id}}: {{.action}} - {{.msg}}"

# Track specific event through pipeline
{container=~"found-footy-.*"} | json | event_id="1234567_goal_45"

# Human-readable log format
{container=~"found-footy-prod-.*"} | json | line_format "[{{.module}}] {{.action}}: {{.msg}}"

# Error logs with error details
{container=~"found-footy-prod-.*", level="ERROR"} | json | line_format "[{{.module}}] {{.action}}: {{.msg}} | error={{.error}}"
```

---

## Context Fields by Module

Each module logs specific context fields beyond the standard fields. Use these for filtering and aggregation:

### download
| Field | Type | Description |
|-------|------|-------------|
| `video_url` | string | Twitter video URL being downloaded |
| `tweet_url` | string | Original tweet URL |
| `file_path` | string | Local file path |
| `size` | int | File size in bytes |
| `hash` | string | Perceptual hash value |
| `duration` | float | Video duration in seconds |
| `width`, `height` | int | Video dimensions |
| `aspect_ratio` | float | Width/height ratio |
| `bitrate` | int | Selected variant bitrate |
| `frame_count` | int | Frames extracted for hashing |
| `confidence` | float | Vision API confidence score |

### upload
| Field | Type | Description |
|-------|------|-------------|
| `event_id` | string | Event identifier |
| `fixture_id` | string | Fixture identifier |
| `s3_url` | string | S3 video URL |
| `new_popularity` | int | Updated popularity score |
| `clusters` | int | Dedup cluster count |
| `input`, `keepers`, `removed` | int | Dedup batch stats |
| `to_upload`, `replacements`, `popularity_bumps` | int | Final dedup summary |

### twitter
| Field | Type | Description |
|-------|------|-------------|
| `query` | string | Twitter search query |
| `max_age_minutes` | int | Search time window |
| `found` | int | Videos found in search |
| `instance` | string | Twitter instance URL used |
| `healthy` | int | Healthy instance count |
| `excluding` | list | URLs to exclude from search |
| `count` | int | Workflow/video count |

### monitor
| Field | Type | Description |
|-------|------|-------------|
| `fixture_id` | string | Fixture being monitored |
| `event_id` | string | Event detected |
| `type` | string | Event type (goal, var_cancelled, etc.) |
| `player` | string | Player involved in event |
| `workflow_id` | string | Temporal workflow ID |
| `workflow_count` | int | Active workflows for event |
| `winner` | string | Match winner (home/away/draw) |
| `count`, `max_count` | int | Completion progress |

### ingest
| Field | Type | Description |
|-------|------|-------------|
| `date` | string | Fixtures date |
| `leagues` | list | League IDs to fetch |
| `count` | int | Fixtures fetched |
| `staging`, `live`, `active`, `skipped` | int | Categorization counts |
| `retention_days` | int | Cleanup retention period |
| `fixtures_deleted`, `videos_deleted` | int | Cleanup counts |

### rag
| Field | Type | Description |
|-------|------|-------------|
| `team_id` | string | Team API ID |
| `team_name` | string | Team display name |
| `team_type` | string | national/club |
| `qid` | string | Wikidata QID |
| `aliases` | list | Generated aliases |
| `search_terms_tried` | int | Wikidata search attempts |
| `desc_quality` | string | Wikidata description quality |
| `rejected`, `accepted` | int | Hallucination filter counts |

### scaler
| Field | Type | Description |
|-------|------|-------------|
| `active_workflows` | int | Running Temporal workflows |
| `workers_running` | int | Worker container count |
| `twitter_running` | int | Twitter container count |
| `active_goals` | int | Active goal events |
| `current`, `target` | int | Scaling counts |
| `service` | string | Service being scaled |
| `healthy`, `unhealthy` | int | Twitter instance health |

### mongo_store
| Field | Type | Description |
|-------|------|-------------|
| `fixture_id` | string | Fixture document ID |
| `event_id` | string | Event document ID |
| `count` | int | Document/video count |
| `duplicate_count` | int | Skipped duplicate URLs |
| `s3_url` | string | Video S3 URL |
| `new_popularity` | int | Updated popularity |
| `workflow_id` | string | Tracked workflow ID |
| `deleted`, `failed` | int | VAR cleanup counts |

### s3_store
| Field | Type | Description |
|-------|------|-------------|
| `bucket` | string | S3 bucket name |
| `s3_key` | string | Object key |
| `goal_id` | string | Goal/event identifier |
| `fixture_id` | string | Fixture identifier |
| `video_count` | int | Videos tagged |

---

## Log Level Guidelines

| Level | Usage |
|-------|-------|
| `DEBUG` | Detailed diagnostic info (individual rejections, frame-level operations) |
| `INFO` | Normal operation milestones (started, completed, counts) |
| `WARNING` | Recoverable issues, degraded operation (fallbacks, retries, filtered hallucinations) |
| `ERROR` | Operation failures requiring attention (API errors, timeouts, missing data) |

### When to Use Each Level

**DEBUG:**
- Individual video filter rejections (resolution, aspect, duration)
- Single hallucination rejections in RAG
- Frame-by-frame hash generation
- Individual Wikidata search attempts

**INFO:**
- Workflow/activity start and completion
- Successful API calls and data operations
- Summary statistics (batch counts, totals)
- State transitions

**WARNING:**
- Using fallback behavior (e.g., fallback_aliases, no_healthy_instances)
- Non-critical failures (single file delete failed in batch)
- Summary of filtered content (hallucinations_filtered)
- Incomplete operations that can continue

**ERROR:**
- API failures (MongoDB, S3, Twitter, external APIs)
- Required resources unavailable
- Operations that cannot complete
- Unhandled exceptions

---

## Adding New Logging

When adding new log statements, follow this checklist:

1. **Choose the right module**: Use existing module constants or create new ones for new components
2. **Use descriptive actions**: Format as `noun_verb` (e.g., `video_downloaded`, `cache_hit`)
3. **Include context**: Always include relevant IDs (fixture_id, event_id, workflow_id)
4. **Use appropriate level**: Follow the level guidelines above
5. **Keep messages concise**: The `msg` should be human-readable but brief
6. **Add to this document**: Update the relevant module section with new actions

### Example Adding New Action

```python
# In activity code
log.info(
    logger,
    MODULE,
    "new_action_name",
    "Brief human-readable description",
    fixture_id=fixture_id,
    event_id=event_id,
    relevant_count=len(items)
)

# Then document in LOGGING.md:
# - `new_action_name` - Brief description (fixture_id, event_id, relevant_count)
```