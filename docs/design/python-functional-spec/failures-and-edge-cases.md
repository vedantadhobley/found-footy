# Python failures and edge cases

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 10. Failure Modes & Recovery

### Twitter Authentication Expiry

**Symptom:** Twitter service returns 503 "Login Required" or 401
"Unauthorized"

**Recovery path:**
1. Selenium/Firefox **refreshes cookies** (via session persistence, ~30s)
2. If refresh fails: **VNC intervention** required (manual re-login
   to Twitter via VNC interface)
3. Workflow continues after auth restored
4. Current attempt may be lost, but TwitterWorkflow retries on next attempt

### LLM Unavailability (joi Down)

**Symptom:** `validate_video_is_soccer()` times out or returns 5xx

**Recovery:**
- Activity has **4 retry attempts** with exponential backoff (3s → 30s max)
- If all 4 fail: video is **FAIL-CLOSED** (rejected, deleted)
- DownloadWorkflow continues (per-video failures don't block batch)
- Telemetry recorded in `_download_stats.ai_validation_failed`

### Video Geo-Restriction (yt-dlp 403)

**Symptom:** `download_single_video()` raises `VideoGeoRestrictedError`

**Handled:** Video marked as failed, counted in
`_download_stats.download_failed`, file deleted. DownloadWorkflow
continues.

### S3 Upload Failure

**Symptom:** `upload_single_video()` fails to PUT object

**Recovery:**
- Activity has **3 retry attempts** with 1.5x backoff (2s, 3s, 4.5s)
- If all 3 fail: video marked as upload failure
- UploadWorkflow continues processing other videos in batch

### MongoDB Write Failure

**Recovery:** Temporal activity retries (2-3 attempts, varies by
activity). If retry exhausted: activity raises, workflow may crash
or be retried by Temporal.

### Twitter Workflow Crash (Mid-Search)

**Recovery:**
- `_download_workflows` array still contains IDs of started downloads
- MonitorWorkflow eventually sees `_monitor_complete = true`, but
  `_download_complete = false`
- Next monitor cycle: event stays visible, Monitor doesn't re-trigger
  Twitter (already started once)
- DownloadWorkflows continue running independently
- If fewer than 10 downloads finish: event gets stuck at
  `_download_complete = false` indefinitely
  - **Workaround:** Manual fixture re-ingest or emergency re-trigger

### Download Workflow Crash (Mid-Download)

**Recovery:**
- Workflow ID is already in `_download_workflows` (count incremented
  at START)
- TwitterWorkflow sees count >= 10 → exits loop
- No videos uploaded (DLWF crashed before signaling UploadWorkflow)
- Event has `_download_complete = true` but 0 videos in S3

### Upload Workflow Crash (Mid-Batch)

**Recovery:**
- Uploaded videos are **already in S3** (PUT succeeded)
- MongoDB `_s3_videos` array is **not updated** (crash before write)
- Fixture shows 0 videos to frontend, but blobs exist in S3
- **Workaround:** Manual MongoDB update or re-trigger upload

### Concurrent Event Modifications (Race Condition)

**Behavior:**
- Both fetch active fixture from MongoDB
- Both attempt to update same event
- Last write wins (MongoDB is last-write-wins, not ACID)

### Duplicate Signal Delivery

**Behavior:**
- Signal handler appends to `_pending_batches` deque each time
- Same batch processed twice
- Videos deduplicated against S3 → second pass bumps popularity

**Outcome:** Benign, no data corruption.

---

## 11. Edge Cases & Corner Behaviors

### Late Goals (90+ minutes)

**Scenario:** Goal scored in 95th minute of match

**Behavior:**
- API returns goal with `elapsed = 95`
- Monitor detects event normally
- Twitter searches with player + team
- System works the same (no special halftime handling for late goals)
- All 15 attempts run regardless of clock time

### VAR Reversal Mid-Discovery

**Timeline:**
- T0: Goal detected, event added to active, monitor debounces
- T1: Twitter search spawned
- T2: 5 DownloadWorkflows have registered
- T3: API removes goal (VAR'd)
- T4: MonitorWorkflow polls again, event not in live

**Behavior:**
- VAR handler adds MonitorWorkflow ID to `_drop_workflows`
- After 3 drops: event **deleted entirely** (S3 videos deleted if
  `_monitor_complete = true`)
- In-flight DownloadWorkflows continue (they have video list cached)
- Remaining uploads to S3 happen, but event is gone → S3 orphaned
  videos

**Mitigation:** UploadWorkflow checks event exists before processing
batch; aborts if deleted.

### Postponement Mid-Monitoring (PST Status)

**Behavior:**
- Monitor sees status PST (treated as ACTIVE)
- Fixture stays in fixtures_active (not moved back to staging)
- Events already detected stay as-is
- If match resumes: Monitor continues polling, detects new goals
- If match cancelled: Status changes to CANC → completion check
  triggers → moves to fixtures_completed

### Simultaneous Events on Same Fixture

**Behavior:**
- Both have different `sequence` numbers
- Event IDs are distinct
- TwitterWorkflow spawned separately for each
- DownloadWorkflows run in parallel (different event IDs, different
  S3 keys)
- UploadWorkflows process separately (different UploadWorkflow IDs)

**Outcome:** Fully parallelized.

### Halftime Handling

**No special behavior:**
- Monitor treats HT status just like 1H or 2H
- Continues polling
- Events continue to be detected
- No "halftime reset" of counters or fields

Events do NOT disappear during halftime — they persist in the API's
events array across the break. Set-based comparison handles them
naturally.

### Event with Null player.id (Early API Updates)

**Behavior:**
- Event is created with `player_id = 0`
- `is_player_known()` returns **false** (0 treated as unknown)
- Event added to active but with `_monitor_workflows = []` (not registered)
- Event stays visible in frontend but won't debounce/trigger Twitter
- When API updates with actual player_id (seconds/minutes later), a
  **new event is created** (new sequence for known player)
- Old "Unknown" event is eventually VAR'd (seen missing for 3 polls)

**File:** `archive/src/activities/monitor.py:27-46`

### Event with Null player.name (But player.id Present)

**Behavior:** Same as above — `is_player_known()` requires both id
and name.

### Backdated Fixtures (Kickoff Already Passed at Ingest Time)

**Behavior:**
- Fixture would be in fixtures_staging (not started)
- At next `pre_activate_upcoming_fixtures` check, it's in the past
- Check: `fixture.date <= now + 30min` → **false**
- Fixture stays in staging, never activated

### Timezone Edge Cases (UTC-12 to UTC+14)

**Behavior:**
- API returns ISO datetime (docs say UTC)
- Assuming UTC: ingest fetches "today" UTC, which may miss today in
  UTC+14 or catch tomorrow in UTC-12

**Mitigation:** Three-day fetch (today/tomorrow/day-after) should
cover all edge cases.

---
