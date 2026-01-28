# Found Footy - Orchestration Model

## 🎯 Core Principle: Decoupled Workflow Architecture

The system uses a **decoupled architecture** where:
- **MonitorWorkflow** handles debouncing and triggers TwitterWorkflow **ONCE** per event (fire-and-forget)
- **TwitterWorkflow** resolves aliases (cache or RAG) then manages 10 search attempts with 1-minute durable timers
- **DownloadWorkflow** downloads, validates, and hashes videos (BLOCKING child)
- **UploadWorkflow** serializes S3 operations per event via deterministic workflow ID

This decoupling allows:
- Twitter searches to run at 1-minute intervals (not tied to Monitor's 30s poll)
- Alias resolution inside TwitterWorkflow (eliminates double fire-and-forget chain)
- Serialized S3 uploads via UploadWorkflow (eliminates race conditions)
- Fixture completion only after all downloads are done

---

## 📊 Event State Machine

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         EVENT LIFECYCLE                                      │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    PHASE 1: DEBOUNCE (Monitor)                        │   │
│  │                                                                        │   │
│  │   _monitor_complete = FALSE                                            │   │
│  │                                                                        │   │
│  │   Each 30s (Monitor poll):                                             │   │
│  │     IF event seen: increment _monitor_count                            │   │
│  │     IF _monitor_count >= 3:                                            │   │
│  │       → start_child_workflow(TwitterWorkflow) [fire-and-forget]        │   │
│  │       → THEN set _monitor_complete = TRUE (after Twitter starts!)      │   │
│  │                                                                        │   │
│  │   RECOVERY: If count=3 AND monitor_complete=FALSE (stuck goal):        │   │
│  │       → retry Twitter start on next poll                               │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                              │                                               │
│                              ▼ (TwitterWorkflow started)                     │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    PHASE 2: TWITTER (Self-Managed)                    │   │
│  │                                                                        │   │
│  │   TwitterWorkflow (~10 min, fire-and-forget from Monitor):             │   │
│  │     1. Resolve aliases: cache lookup OR RAG pipeline (~30-90s)         │   │
│  │     2. FOR attempt IN [1..10]:                                         │   │
│  │       → check_event_exists (VAR check - abort if removed)              │   │
│  │       → Search all aliases: "Salah Liverpool", "Salah LFC", ...        │   │
│  │       → Dedupe videos, save to _discovered_videos                      │   │
│  │       → IF videos: start DownloadWorkflow (BLOCKING child)             │   │
│  │           → DownloadWorkflow: check_event_exists (abort if VAR'd)      │   │
│  │           → DownloadWorkflow: download → validate → hash               │   │
│  │           → DownloadWorkflow calls UploadWorkflow (BLOCKING, serialized)│   │
│  │           → DownloadWorkflow: increment_twitter_count                  │   │
│  │       → ELSE: increment_twitter_count directly                         │   │
│  │       → IF attempt < 10: sleep(1 minute) ← DURABLE TIMER               │   │
│  │     WHEN _twitter_count reaches 10:                                    │   │
│  │       → _download_complete = TRUE (set atomically by increment)         │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                              │                                               │
│                              ▼ (_download_complete = TRUE)                    │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         PHASE 3: COMPLETE                             │   │
│  │                                                                        │   │
│  │   When fixture status = FT/AET/PEN AND:                                │   │
│  │     ALL events have _monitor_complete = TRUE                           │   │
│  │     ALL events have _download_complete = TRUE                           │   │
│  │                                                                        │   │
│  │   → Fixture moves to fixtures_completed                                │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 🔢 Event Tracking Fields

| Field | Set By | When | Purpose |
|-------|--------|------|---------|
| `_monitor_workflows` | Monitor | Each poll when event seen | Array of workflow IDs (debounce via array length) |
| `_monitor_complete` | TwitterWorkflow | At VERY START of workflow | Debounce finished, Twitter triggered |
| `_twitter_aliases` | TwitterWorkflow | After alias resolution | Team search variations |
| `_download_workflows` | DownloadWorkflow | At VERY START of each download | Array of workflow IDs (tracks attempts) |
| `_download_complete` | UploadWorkflow | When len(_download_workflows) >= 10 | All attempts finished |

---

## 🔄 Workflow Responsibilities

### MonitorWorkflow (Scheduled Every 30 Seconds)
- Polls active fixtures from API
- Adds workflow ID to `_monitor_workflows` array for seen events (via $addToSet)
- When array length reaches 3: **Triggers TwitterWorkflow** (fire-and-forget)
- TwitterWorkflow sets `_monitor_complete = TRUE` at its start (not Monitor)
- Includes **CASE 2 recovery**: if `workflows>=3 AND monitor_complete=FALSE` (stuck), retries Twitter start
- Checks fixture completion eligibility
- **Does NOT manage Twitter retries** (that's TwitterWorkflow's job)
- **NO execution timeout** — uses SKIP overlap policy (if still running, next scheduled is skipped)

### TwitterWorkflow (Triggered by Monitor, ~10 min)
- **Sets `_monitor_complete = TRUE` at VERY START** (before any other work)
- **Resolves aliases at start**: cache lookup OR full RAG pipeline
- **WHILE `get_download_workflow_count() < 10`**: loop with durable timers (1-min spacing)
- Builds search queries: `{player_last} {alias}` for each alias
- Deduplicates videos across aliases and previous attempts
- **ALWAYS starts DownloadWorkflow (BLOCKING child)** - even with 0 videos
- `_download_complete` is set by UploadWorkflow when `len(_download_workflows) >= 10`

### DownloadWorkflow (Triggered by TwitterWorkflow, BLOCKING)
- **Registers workflow ID at VERY START** via `register_download_workflow`
- **Checks event exists** (VAR check - aborts if removed)
- Downloads videos from Twitter URLs (parallel)
- Applies duration filter (>3s to 60s)
- MD5 batch dedup (within downloaded batch only)
- Validates soccer content via vision model (Qwen3-VL)
- Computes perceptual hash for deduplication (dense 0.25s sampling)
- **ALWAYS signals UploadWorkflow** (even with 0 videos - for completion tracking)

### UploadWorkflow (Signal-with-Start Pattern)
- **Workflow ID: `upload-{event_id}`** - namespace-scoped, ONE per event
- **Receives videos via `add_videos` signal** - queued in FIFO deque
- **Checks event exists before each batch** (VAR check - aborts workflow if removed)
- Processes batches: fetch S3 state, MD5/perceptual dedup, upload
- Updates MongoDB `_s3_videos` array
- Recalculates video ranks
- Cleans up temp files
- **Idles for 5 minutes** waiting for more signals, then completes

**Signal-with-Start Pattern:**
- `queue_videos_for_upload` activity uses Temporal Client API (not child workflow)
- `client.start_workflow(..., start_signal="add_videos", start_signal_args=[videos])`
- If workflow not running: START it AND deliver signal atomically
- If workflow already running: just SIGNAL it (videos added to queue)
- **Key insight**: Client API workflow IDs are namespace-scoped (global)
- Child workflow IDs are parent-scoped (would cause "already started" errors)
- Multiple DownloadWorkflows for SAME event → ONE UploadWorkflow, FIFO queue
- Multiple events → PARALLEL UploadWorkflows (different IDs)

---

## ⏱️ Timeline Example

```
T+0:00  Goal scored! Event appears in API
T+0:30  Monitor poll #1 → _monitor_workflows = ["monitor-T0:30"]
T+1:00  Monitor poll #2 → _monitor_workflows = ["monitor-T0:30", "monitor-T1:00"]
T+1:30  Monitor poll #3 → _monitor_workflows = [..., "monitor-T1:30"] (len=3)
        → TwitterWorkflow triggered (fire-and-forget)
        
T+1:35  TwitterWorkflow starts:
        → set_monitor_complete(true) ← FIRST THING
        → get_cached_team_aliases(40) → HIT: ["LFC", "Reds", "Liverpool"]
        → save to _twitter_aliases in event
        
T+1:40  TwitterWorkflow Attempt 1:
        → get_download_workflow_count() returns 0
        → Search "Salah LFC" → 3 videos
        → Search "Salah Reds" → 2 videos (1 dup)
        → Search "Salah Liverpool" → 2 videos (1 new)
        → Dedupe → 5 unique videos
        → Start DownloadWorkflow (BLOCKING)
          → register_download_workflow() ← FIRST THING (len=1)
          → Download 5 videos
          → AI validation → 4 pass
          → Generate hashes
          → Signal UploadWorkflow (upload-{event_id})
            → Fetch fresh S3 state (empty)
            → Upload 4 videos to S3
            → Save to MongoDB
            → check_and_mark_download_complete() → len=1, not complete
          
T+2:40  Attempt 1 complete, sleep(1 min)

T+3:40  TwitterWorkflow Attempt 2:
        → get_download_workflow_count() returns 1
        → Search same aliases (exclude already-found URLs)
        → 1 new video found
        → Start DownloadWorkflow (BLOCKING)
          → register_download_workflow() ← FIRST THING (len=2)
          → Download → validate → hash
          → Signal UploadWorkflow (upload-{event_id})
            → Fetch fresh S3 state (4 videos)
            → Perceptual dedup → 1 new unique
            → Upload 1 video
            → check_and_mark_download_complete() → len=2, not complete
          
T+4:40  Attempt 2 complete, sleep(1 min)

... (attempts 3-9 similar) ...

T+11:40 TwitterWorkflow Attempt 10:
        → get_download_workflow_count() returns 9
        → Search same aliases
        → 0 new videos found
        → Start DownloadWorkflow (BLOCKING, even with 0 videos!)
          → register_download_workflow() ← FIRST THING (len=10)
          → Signal UploadWorkflow (empty batch)
            → check_and_mark_download_complete() → len=10!
            → _download_complete = TRUE
        
T+12:00 Monitor poll:
        → Fixture status = FT
        → All events have _monitor_complete = TRUE
        → All events have _download_complete = TRUE
        → Fixture moves to fixtures_completed
```

---

## 🎯 Key Design Decisions

### Why Twitter directly from Monitor (no separate RAGWorkflow)?
Previously: Monitor → RAGWorkflow → TwitterWorkflow (double fire-and-forget)
Problem: Two fire-and-forget hops caused duplicate workflows due to Temporal ID reuse timing
Solution: Twitter resolves aliases at start, single fire-and-forget from Monitor

### Why Signal-with-Start for UploadWorkflow?
Problem: Multiple parallel DownloadWorkflows for SAME event need serialized uploads
Failed approaches:
- Child workflow with same ID → "Workflow execution already started" error
- Child workflow IDs are parent-scoped, not namespace-scoped
Solution: Activity uses Client API's signal-with-start pattern
- Workflow ID `upload-{event_id}` is namespace-scoped (global)
- Signal-with-start: start if not exists, signal if exists (atomic)
- Videos queued in FIFO deque, processed in order
Benefit: No errors, proper serialization, parallel uploads for DIFFERENT events

### Why 10 Twitter attempts with 1-min spacing?
Goal videos appear over 5-15 minutes. More frequent searches = more videos captured.
Blocking downloads ensure reliable completion tracking via `_twitter_count`.

### Why Downloads are BLOCKING (not fire-and-forget)?
- `_twitter_count` must accurately track completed downloads
- If fire-and-forget, count would increment before downloads finish
- Fixture could complete before all videos are uploaded
- BLOCKING ensures completion tracking is reliable

### Why RAG resolution inside TwitterWorkflow?
- Eliminates double fire-and-forget chain (was causing duplicate workflows)
- Aliases are usually cached (pre-warmed during ingestion)
- Cache miss is rare (~30-90s RAG pipeline)
- Simpler architecture with fewer workflows to coordinate

---

## 🛡️ Race Condition Prevention

### S3 Duplicate Prevention
**Problem**: Two DownloadWorkflows find the same video, both upload it
**Solution**: UploadWorkflow with deterministic ID serializes uploads per event
**Guarantee**: Only ONE UploadWorkflow runs at a time per event

### Fixture Completion Prevention  
**Problem**: Fixture moves to completed while downloads still running
**Solution**: `_download_complete` only set when `_twitter_count` reaches 10
**Guarantee**: All 10 download attempts must complete before fixture can complete

### Alias Resolution Race
**Problem**: Cache might be stale if team just played for first time
**Solution**: TwitterWorkflow checks cache, falls back to full RAG pipeline
**Guarantee**: Aliases are always resolved before search starts

### VAR Reversal Handling
**Problem**: Goal gets VAR'd while downstream workflows are running
**Solution**: Multi-layer existence checks:
1. **TwitterWorkflow**: `check_event_exists` at START of each attempt (1-10 loop)
2. **DownloadWorkflow**: `check_event_exists` at START of workflow
3. **UploadWorkflow**: `fetch_event_data` returns "event_not_found" → abort workflow
4. **Monitor**: Decrements `_monitor_count` when event disappears, deletes at 0
**Guarantee**: Workflows abort gracefully, no orphaned S3 uploads

---

## 👤 Unknown Player Handling

When a goal is first detected, the API may not yet have the scorer identified.

**Detection**: `is_player_known()` checks if player name is None, empty, or "Unknown"

**Behavior**:
- Event is created with `_monitor_count = 0` (not 1)
- Event appears in frontend but shows as "Unknown Player"
- **Debouncing is frozen** - count stays at 0, won't progress to 3
- **No Twitter search triggered** - player name is required for search
- API data (time, assist, comments) still synced on each poll

**Resolution**:
When API identifies the player, `player_id` changes → new `_event_id` generated.
Old "Unknown" event is removed via normal VAR logic (count decrements to 0).
New event starts fresh debounce with known player.

**Frontend Use**:
- `_monitor_count === 0` → Unknown player, waiting for identification
- `_monitor_count >= 1` → Player is known, debouncing in progress

---

## 📊 API Data Synchronization

Event data from API can change after initial detection:
- **time**: Minute may drift (e.g., 23' → 23+2')
- **assist**: Assister may be added later
- **comments**: Additional context may appear

**Solution**: `update_event_stable_count()` syncs these fields on EVERY poll,
not just during debounce. This ensures MongoDB stays current with API changes.
