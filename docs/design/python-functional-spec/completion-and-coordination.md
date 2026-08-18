# Python completion and coordination behavior

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 8. Fixture Completion Behavior

### Preconditions

Fixture can move from **fixtures_active → fixtures_completed** only
when:

1. **Status is completed** (FT, AET, PEN, CANC, ABD, WO, AWD)
2. **All valid events** have `_monitor_complete == true` AND
   `_download_complete == true`
3. **Completion counter** has reached threshold:
   - First poll with completed status: `_completion_count = 1`
   - Subsequent polls: increment (capped at 3)
   - Move when `count >= 3` **OR** winner data exists
     (`teams.home.winner` or `teams.away.winner` is not null)

**File:** `archive/src/activities/monitor.py:366-482`
(complete_fixture_if_ready)

### Completion Detection

MonitorWorkflow checks for completion after processing all events:

```python
if fixture_status in completed_statuses:
  was_completed = complete_fixture_if_ready(fixture_id)
  if was_completed:
    # Log match summary telemetry
    # Cleanup temp directories
```

The **3-poll debounce** handles API delays in confirming
winner/score finalization (especially important for penalty
shootouts).

### Match Completion Summary

When fixture completes, activity logs a **per-match telemetry
summary**:

```
{
  fixture_id, league_id, league_name,
  home_team, away_team, score_home, score_away,
  goals_total,
  videos_captured_total,
  coverage_rate,
  goals_with_zero_videos,
  top_failure_class,
  failure_classes_total,
  search_attempts_total,
  time_to_first_s3_p50_s
}
```

Logged with action `"match_completed_summary"`.

**File:** `archive/src/activities/monitor.py:49-144`

### Coverage SLO Check

For fixtures in tracked leagues (top-5 + UEFA + continental), if
coverage rate < 50%, a **separate warning log line** is emitted
(alertable in Grafana):

```
log.warning("match_below_slo",
  slo_threshold=0.5, coverage_rate=0.33, ...)
```

**File:** `archive/src/activities/monitor.py:452-472`

### State Transition

Fixture document is moved to **fixtures_completed** with:
- All fields from fixtures_active preserved
- New field: `_completed_at = now`
- All events have `_monitor_complete` and `_download_complete` true

### Temp Directory Cleanup

MonitorWorkflow then cleans up temp directories for the completed
fixture:

```
cleanup_fixture_temp_dirs(fixture_id)
  -> removes /tmp/found-footy/* files for this fixture
```

**File:** `archive/src/workflows/monitor_workflow.py:269-283`

---

## 9. Cross-Workflow Coordination

### Coordination Mechanisms

| From | To | Mechanism | Wait? | Notes |
| --- | --- | --- | --- | --- |
| **Ingest** | **RAG** | `workflow.execute_activity()` | Yes | Per-team, blocking, 2 retries max |
| **Monitor** | **Twitter** | `start_child_workflow()` | No | Fire-and-forget, REJECT_DUPLICATE |
| **Twitter** | **Download** | `start_child_workflow()` | No | Fire-and-forget, 1 per attempt (up to 15) |
| **Download** | **Upload** | `signal_with_start()` | No | FIFO queue via Temporal signals |
| **Monitor** | **Upload** | `check_and_mark_download_complete()` | No | Failsafe, idle-timeout backup |
| Everything | **Frontend** | `notify_frontend_refresh()` | No | Best-effort SSE broadcast |

### Ingest → RAG

**Synchronous, per-team:**
- `get_team_aliases(team_id, team_name, team_type, country)`
- Blocks ingest workflow until result (cache hit or RAG complete)
- Retry: 2 attempts max
- Timeout: 90 seconds
- Failure: logs warning, continues

**File:** `archive/src/workflows/ingest_workflow.py:288-304`

### Monitor → Twitter

**Fire-and-forget child workflow:**
- Spawned when `len(_monitor_workflows) >= 3 AND _monitor_complete == false`
- MonitorWorkflow doesn't wait for TwitterWorkflow to complete
- Temporal `REJECT_DUPLICATE` prevents accidental re-spawning same event
- Parent close policy `ABANDON`: TwitterWorkflow continues even if
  Monitor dies

**File:** `archive/src/workflows/monitor_workflow.py:184-220`

### Twitter → Download

**Fire-and-forget child workflow (×10):**
- One DownloadWorkflow spawned per Twitter attempt (up to 15 total)
- TwitterWorkflow doesn't wait for download to complete
- Parent close policy `ABANDON`
- DownloadWorkflow registers itself at START (adds to
  `_download_workflows` count)

**File:** `archive/src/workflows/twitter_workflow.py:479-506`

### Download → Upload (Signal-with-Start)

**Async signal delivery, FIFO queue:**
- DownloadWorkflow calls `queue_videos_for_upload()` activity
- Activity uses Temporal client to signal-with-start UploadWorkflow
- If no UploadWorkflow exists: starts one, immediately delivers signal
- If exists: enqueues signal to `_pending_batches` deque
- Temporal guarantees signal **delivery order** (FIFO)
- UploadWorkflow processes signals **one at a time** (sequential,
  no parallelism per event)

**File:** `archive/src/workflows/download_workflow.py:580-632`

### Monitor → Upload (Failsafe)

**Idle-timeout backup:**
- UploadWorkflow waits up to 5 minutes for new signals
- On timeout, runs `check_and_mark_download_complete()` as failsafe

**File:** `archive/src/workflows/upload_workflow.py:94-109`

### Everything → Frontend (SSE Broadcast)

**Best-effort notification:**
- Called by Ingest, Monitor (if events triggered), Twitter (on
  completion)
- Posts to `/api/found-footy/refresh` (legacy vedanta-systems endpoint)
- Also posts to `/api/v1/internal/notify` (new found-footy FastAPI endpoint)
- Both paths run unconditionally; failure of either is logged but
  doesn't fail activity

**File:** `archive/src/activities/monitor.py:764-820`

---
