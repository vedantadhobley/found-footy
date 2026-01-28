# Workflow-ID-Based Tracking Refactor

## Overview

This document outlines a significant architectural change to how we track workflow execution and determine event completion. We are moving from **counter-based tracking** (`_monitor_count`, `_twitter_count`) to **workflow-ID-based tracking** (`_monitor_workflows`, `_download_workflows`).

---

## The Problem

### Root Cause: Counter Increment Failures

A few weeks ago, we encountered a critical bug where events would run "forever" because the `_twitter_count` counter failed to reach 10, and `_twitter_complete` was never set to `true`.

**What happened:**
1. TwitterWorkflow triggers DownloadWorkflow 10 times
2. DownloadWorkflow (or UploadWorkflow) is supposed to call `increment_twitter_count` after each attempt
3. If the increment fails (network issue, crash, race condition), the counter stays stuck
4. MonitorWorkflow sees `_twitter_complete = false` and keeps trying to restart workflows
5. The event never completes

**Why counters are fragile:**
- `$inc` operations can fail silently
- If a workflow crashes AFTER doing work but BEFORE incrementing, we lose the count
- No audit trail - we can't tell which workflows actually ran
- Race conditions when multiple workflows try to increment simultaneously

### Current Workarounds (Hacky)

We've implemented several workarounds that add complexity:

1. **`check_twitter_workflow_running`** - Query Temporal to see if workflow exists
2. **Stuck event detection** - MonitorWorkflow checks for `_monitor_complete=true` but `_twitter_complete=false`
3. **Double-increment protection** - UploadWorkflow and DownloadWorkflow both try to increment
4. **Conditional UploadWorkflow calls** - Only call if there are videos to upload (creates two code paths)

---

## The Solution: Workflow-ID Arrays

Instead of incrementing counters, we **register workflow IDs in arrays**. The array length IS the count. Completion is determined by array membership, not a number reaching a threshold.

### Core Principle

```
OLD: _twitter_count = 7  (how did we get here? which workflows ran?)
NEW: _download_workflows = ["download1-...", "download2-...", ..., "download7-..."]  (audit trail!)
```

### Key Benefits

| Aspect | Counter-Based (Old) | Workflow-ID-Based (New) |
|--------|---------------------|-------------------------|
| **Auditability** | None - just a number | Full - see exactly which workflows ran |
| **Failure Recovery** | Lost counts on crash | Idempotent - re-register same ID is no-op |
| **Race Conditions** | `$inc` can race | `$addToSet` is atomic and idempotent |
| **Debugging** | "Why is count stuck at 7?" | "download8 never registered - check its logs" |
| **Completion Check** | Hope the increment worked | Count array length - source of truth |

---

## Model Changes

### Current Event Fields (to be replaced)

```python
# src/data/models.py - EventFields class

# OLD - Counter-based
MONITOR_COUNT = "_monitor_count"       # Integer: 0-3
TWITTER_COUNT = "_twitter_count"       # Integer: 0-10
```

### New Event Fields

```python
# src/data/models.py - EventFields class

# NEW - Workflow-ID-based tracking
MONITOR_WORKFLOWS = "_monitor_workflows"     # Array of MonitorWorkflow IDs (for trigger)
DOWNLOAD_WORKFLOWS = "_download_workflows"   # Array of DownloadWorkflow IDs (for completion)
DROP_WORKFLOWS = "_drop_workflows"           # Array of MonitorWorkflow IDs (for VAR removal)

# KEEP - These remain as derived booleans
MONITOR_COMPLETE = "_monitor_complete"       # True when len(_monitor_workflows) >= 3
TWITTER_COMPLETE = "_twitter_complete"       # True when len(_download_workflows) >= 10
```

### VAR Removal Logic (`_drop_workflows`)

When a goal is VAR'd (removed from the API), we need to debounce before deleting to handle API glitches.

**Old Approach (Counter-based):**
```
Event present: count UP (1 → 2 → 3)
Event missing: count DOWN (3 → 2 → 1 → 0 → DELETE)
```

**New Approach (Workflow-ID-based):**
```
Event SEEN:    Clear _drop_workflows entirely → []
Event MISSING: $addToSet workflow_id to _drop_workflows
               If len(_drop_workflows) >= 3 → DELETE event
```

Key differences:
1. **No up/down counting** - Binary state: seen = clear all, missing = accumulate
2. **Full reset on reappearance** - If event flickers back, we reset completely (not decrement)
3. **Idempotent** - Same workflow ID won't be added twice (`$addToSet`)
4. **3 unique misses required** - Must be missing for 3 unique monitor workflow runs

**Example Timeline:**
```
Monitor-1: Event SEEN    → _drop_workflows = []
Monitor-2: Event MISSING → _drop_workflows = [monitor-2]
Monitor-3: Event SEEN    → _drop_workflows = []           ← Full reset!
Monitor-4: Event MISSING → _drop_workflows = [monitor-4]
Monitor-5: Event MISSING → _drop_workflows = [monitor-4, monitor-5]
Monitor-6: Event MISSING → _drop_workflows = [monitor-4, monitor-5, monitor-6] → DELETE
```

### Example Event Document

```javascript
{
  "_event_id": "1379194_45_343684_Goal_1",
  
  // NEW: Workflow tracking arrays
  "_monitor_workflows": [
    "monitor-scheduled-2026-01-27T15:30:00Z",
    "monitor-scheduled-2026-01-27T15:30:30Z",
    "monitor-scheduled-2026-01-27T15:31:00Z"
  ],
  "_download_workflows": [
    "download1-Everton-Barry-1379194_45_343684_Goal_1",
    "download2-Everton-Barry-1379194_45_343684_Goal_1",
    // ... up to 10
  ],
  "_drop_workflows": [],  // Empty when event is present, accumulates when missing
  
  // KEEP: Derived completion booleans
  "_monitor_complete": true,
  "_twitter_complete": false,
  
  // KEEP: Other fields unchanged
  "_first_seen": "2026-01-27T15:30:00Z",
  "_twitter_search": "Barry Everton",
  "_discovered_videos": [...],
  "_s3_videos": [...]
}
```

---

## Workflow Changes

### Understanding the Current UploadWorkflow Architecture

Before diving into changes, it's important to understand how the UploadWorkflow currently works, as it's central to this refactor.

**Signal-With-Start Pattern:**

The UploadWorkflow uses Temporal's `signal-with-start` pattern for serialization:

```
DownloadWorkflow-1                         UploadWorkflow (ID: upload-{event_id})
      │                                              │
      │──queue_videos_for_upload─────────────────────▶│ (doesn't exist yet)
      │   (signal-with-start)                        │
      │                                              │ STARTS workflow
      │                                              │ DELIVERS signal with videos
      │                                              │
      │                                              │ processes batch...
      │                                              │
      │                                              │ waits for more signals...
      │                                              │ (5 min idle timeout)

DownloadWorkflow-2                                   │
      │                                              │
      │──queue_videos_for_upload─────────────────────▶│ (already exists!)
      │   (signal-with-start)                        │
      │                                              │ just DELIVERS signal
      │                                              │ (doesn't start new workflow)
```

**Key Mechanics:**

1. **Deterministic Workflow ID**: `upload-{event_id}` - all DownloadWorkflows for the same event signal the SAME UploadWorkflow

2. **Signal-with-start**: The `client.start_workflow()` call with `start_signal`:
   - If workflow doesn't exist → starts it AND delivers the signal
   - If workflow exists → just delivers the signal (idempotent)

3. **FIFO queue**: Signals append to `_pending_batches` deque, main loop pops from left

4. **5-minute idle timeout**: After processing a batch, waits up to 5 minutes for the next signal. If no signal arrives, workflow exits.

5. **Empty batch handling**: Already supported - `_process_batch` returns early if `videos=[]`

---

### 1. MonitorWorkflow

**Current Flow:**
```
Poll API → Process events → Increment _monitor_count → If count >= 3: start TwitterWorkflow → set _monitor_complete
```

**New Flow:**
```
Poll API → Process events → Add workflow ID to _monitor_workflows → 
If len >= 3 AND _monitor_complete = false: start TwitterWorkflow →
TwitterWorkflow sets _monitor_complete = true (confirms it started)
```

**Changes in `src/activities/monitor.py`:**

```python
# OLD
new_count = current_count + 1
store.update_event_stable_count(fixture_id, event_id, new_count, ...)
if new_count >= 3:
    trigger_twitter = True
# Later: set _monitor_complete = true after Twitter starts

# NEW
store.add_monitor_workflow(fixture_id, event_id, monitor_workflow_id)  # $addToSet
workflow_count = store.get_monitor_workflow_count(fixture_id, event_id)

# Spawn Twitter ON the 3rd monitor OR if previous spawn failed
if workflow_count >= 3 and not monitor_complete:
    trigger_twitter = True
# Twitter workflow itself sets _monitor_complete = true
```

**Changes in `src/workflows/monitor_workflow.py`:**
- Pass the current workflow ID (`workflow.info().workflow_id`) to `process_fixture_events`

**Changes in `src/workflows/twitter_workflow.py`:**
- At the START of TwitterWorkflow: set `_monitor_complete = true`
- This ensures the flag is only set when Twitter actually starts running

**Why Twitter sets `_monitor_complete`:**
- Ensures the flag is only set when Twitter workflow ACTUALLY STARTS
- If Twitter fails to start (Temporal issue, etc.), `_monitor_complete` stays false
- Next monitor sees `len >= 3` but `complete = false` → retries spawn
- Audit trail: we can see if there's a mismatch (3+ monitors but not complete)

**Debounce Timing:**
```
Monitor-1 (T+0:00):  NEW event discovered → create with _monitor_workflows=[workflow-1] → count=1 → no action
Monitor-2 (T+0:30):  EXISTING event → $addToSet workflow-2 → count=2 → no action  
Monitor-3 (T+1:00):  EXISTING event → $addToSet workflow-3 → count=3 → len >= 3 AND not complete → SPAWN TWITTER
                     TwitterWorkflow starts → sets _monitor_complete = true
Monitor-4 (T+1:30):  EXISTING event → $addToSet workflow-4 → count=4 → len >= 3 BUT complete = true → no action
```

**IMPORTANT:** The workflow that DISCOVERS a new event must also be added to `_monitor_workflows`.
This is done by passing `initial_monitor_workflows=[workflow_id]` to `create_new_enhanced_event()`.

**Failure Recovery:**
```
Monitor-3 (T+1:00):  add to array → count=3 → spawn Twitter → FAILS TO START
                     _monitor_complete still false
Monitor-4 (T+1:30):  add to array → count=4 → len >= 3 AND not complete → retry spawn Twitter
                     TwitterWorkflow starts → sets _monitor_complete = true
```

---

### 2. TwitterWorkflow

**Current Flow:**
```python
for attempt in range(1, 11):
    # Search Twitter
    if videos_found:
        start_child_workflow(DownloadWorkflow, ...)
    else:
        increment_twitter_count(...)  # Increment ourselves since no download
    sleep(60 seconds)
```

**New Flow:**
```python
attempt = 0
max_attempts = 15  # Safety limit (should only need 10, but handles start failures)

while True:
    attempt += 1
    if attempt > max_attempts:
        break  # Safety exit - prevents infinite loops
    
    download_count = get_download_workflow_count(...)
    if download_count >= 10:
        break  # All 10 download workflows have registered - we're done
    
    # Search Twitter
    # ALWAYS start DownloadWorkflow - it registers itself and signals UploadWorkflow
    start_child_workflow(DownloadWorkflow, videos=videos_found)  # videos may be empty
    
    sleep(60 seconds)
```

**Key Changes:**
- Replace `for` loop with `while` loop that checks `_download_workflows` array length
- Add max attempts safety limit (15) to prevent infinite loops if starts keep failing
- ALWAYS call DownloadWorkflow (even with 0 videos found)
- Remove all `increment_twitter_count` calls - DownloadWorkflow handles everything
- If a DownloadWorkflow fails to start, it doesn't register, count stays low, we try again next iteration

---

### 3. DownloadWorkflow

**Current Flow:**
```python
# Start
download_videos(...)
validate_videos(...)

if videos_to_upload:
    signal_upload_workflow(...)  # Upload handles increment
else:
    increment_twitter_count(...)  # We handle increment ourselves
```

**New Flow:**
```python
# FIRST THING: Register ourselves (we're now running!)
register_download_workflow(fixture_id, event_id, workflow_id)  # Uses $addToSet

download_videos(...)
validate_videos(...)

# ALWAYS signal UploadWorkflow - it handles the completion check
signal_upload_workflow(videos_to_upload)  # May be empty list, that's fine
```

**Key Changes:**
- **Register workflow ID at the START** - proves this workflow actually started running
- Uses `$addToSet` so re-running the same workflow doesn't double-count
- ALWAYS call UploadWorkflow (even with empty video list)
- Remove the conditional `increment_twitter_count` branch
- Simpler flow: register → download → validate → signal (always)

---

### 4. UploadWorkflow

**Current Flow:**
```python
# Signal-based FIFO queue with 5-minute idle timeout
while True:
    await wait_condition(pending_batches > 0, timeout=5min)
    if timeout:
        break  # Exit after 5 minutes of no signals
    
    process_batch(...)
    increment_twitter_count(fixture_id, event_id, 10)
    if result["marked_complete"]:
        log("All 10 attempts complete")
```

**New Flow:**
```python
# Signal-based FIFO queue with 5-minute idle timeout
while True:
    await wait_condition(pending_batches > 0, timeout=5min)
    if timeout:
        # FAILSAFE: Check completion one final time before exiting
        await check_and_mark_twitter_complete(fixture_id, event_id)
        break  # Exit after 5 minutes of no signals
    
    process_batch(...)
    
    # Check completion after each batch
    download_count = get_download_workflow_count(fixture_id, event_id)
    if download_count >= 10:
        mark_twitter_complete(fixture_id, event_id)
        log("All 10 download workflows registered - marking complete")
```

**Key Changes:**
- Remove `increment_twitter_count` activity entirely
- Check `_download_workflows` array length to determine completion
- Set `_twitter_complete = true` when 10 download workflows are registered
- **FAILSAFE**: Before exiting on idle timeout, check one final time if 10 download workflows are registered and mark complete if so

**Why the Failsafe?**

The UploadWorkflow stays alive for 5 minutes after the last signal. In normal operation:
- Download workflow 10 signals UploadWorkflow
- UploadWorkflow processes the batch and checks count → sees 10, marks complete
- UploadWorkflow waits 5 minutes, no more signals, exits

But if something goes wrong (e.g., the check-after-batch fails, network hiccup, etc.):
- All 10 download workflows have registered themselves in `_download_workflows`
- But `_twitter_complete` wasn't set for some reason
- The failsafe catches this: before exiting, check one more time

This is a belt-and-suspenders approach. The array is the source of truth - if it has 10 entries, the event IS complete, so we should mark it as such.

**Integration with Signal-With-Start:**

With our new approach, EVERY DownloadWorkflow signals UploadWorkflow (even with 0 videos):

| Scenario | Current Behavior | New Behavior |
|----------|------------------|--------------|
| 3 videos found | Signal with 3 videos | Signal with 3 videos |
| 0 videos found | Skip signal, increment count ourselves | Signal with 0 videos (no-op upload, but completion check runs) |

This means:
- UploadWorkflow stays alive for the full ~10 minutes (signals every ~1 min)
- Completion check runs after EVERY signal (not just ones with videos)
- The 5-minute idle timeout acts as final cleanup, not as a race against missing signals

---

## MongoDB Helper Functions

New functions needed in `src/data/mongo_store.py`:

```python
def add_monitor_workflow(self, fixture_id: int, event_id: str, workflow_id: str) -> bool:
    """
    Add a MonitorWorkflow ID to the event's _monitor_workflows array.
    Uses $addToSet for idempotency - adding the same ID twice is a no-op.
    """
    result = self.fixtures_active.update_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {"$addToSet": {f"events.$.{EventFields.MONITOR_WORKFLOWS}": workflow_id}}
    )
    return result.modified_count > 0

def add_download_workflow(self, fixture_id: int, event_id: str, workflow_id: str) -> bool:
    """
    Add a DownloadWorkflow ID to the event's _download_workflows array.
    Uses $addToSet for idempotency - adding the same ID twice is a no-op.
    """
    result = self.fixtures_active.update_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {"$addToSet": {f"events.$.{EventFields.DOWNLOAD_WORKFLOWS}": workflow_id}}
    )
    return result.modified_count > 0

def get_monitor_workflow_count(self, fixture_id: int, event_id: str) -> int:
    """Return the number of MonitorWorkflows that have processed this event."""
    fixture = self.fixtures_active.find_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {f"events.$": 1}
    )
    if fixture and fixture.get("events"):
        return len(fixture["events"][0].get(EventFields.MONITOR_WORKFLOWS, []))
    return 0

def get_download_workflow_count(self, fixture_id: int, event_id: str) -> int:
    """Return the number of DownloadWorkflows that have run for this event."""
    fixture = self.fixtures_active.find_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {f"events.$": 1}
    )
    if fixture and fixture.get("events"):
        return len(fixture["events"][0].get(EventFields.DOWNLOAD_WORKFLOWS, []))
    return 0

def mark_twitter_complete(self, fixture_id: int, event_id: str) -> bool:
    """Set _twitter_complete = true for an event."""
    result = self.fixtures_active.update_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {"$set": {
            f"events.$.{EventFields.TWITTER_COMPLETE}": True,
            f"events.$.{EventFields.TWITTER_COMPLETED_AT}": datetime.now(timezone.utc)
        }}
    )
    return result.modified_count > 0

def mark_monitor_complete(self, fixture_id: int, event_id: str) -> bool:
    """
    Set _monitor_complete = true for an event.
    Called by TwitterWorkflow at the VERY START to confirm it actually started.
    """
    result = self.fixtures_active.update_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {"$set": {f"events.$.{EventFields.MONITOR_COMPLETE}": True}}
    )
    return result.modified_count > 0

def get_monitor_complete(self, fixture_id: int, event_id: str) -> bool:
    """Return the current value of _monitor_complete for an event."""
    fixture = self.fixtures_active.find_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id},
        {f"events.$": 1}
    )
    if fixture and fixture.get("events"):
        return fixture["events"][0].get(EventFields.MONITOR_COMPLETE, False)
    return False
```

---

## Activities Changes

### New Activities Needed

```python
# src/activities/monitor.py

@activity.defn
async def register_monitor_workflow(
    fixture_id: int, 
    event_id: str, 
    workflow_id: str
) -> Dict[str, Any]:
    """
    Register a MonitorWorkflow as having processed this event.
    Uses $addToSet for idempotency.
    Returns count AND current _monitor_complete status for spawn decision.
    """
    store = MongoStore()
    success = store.add_monitor_workflow(fixture_id, event_id, workflow_id)
    count = store.get_monitor_workflow_count(fixture_id, event_id)
    monitor_complete = store.get_monitor_complete(fixture_id, event_id)
    return {
        "success": success, 
        "count": count, 
        "monitor_complete": monitor_complete,
        "should_trigger_twitter": count >= 3 and not monitor_complete
    }


# src/activities/twitter.py

@activity.defn
async def set_monitor_complete(
    fixture_id: int, 
    event_id: str
) -> Dict[str, Any]:
    """
    Set _monitor_complete = true for an event.
    Called by TwitterWorkflow at the VERY START to confirm it actually started.
    """
    store = MongoStore()
    success = store.mark_monitor_complete(fixture_id, event_id)
    return {"success": success}


# src/activities/download.py

@activity.defn
async def register_download_workflow(
    fixture_id: int, 
    event_id: str, 
    workflow_id: str
) -> Dict[str, Any]:
    """
    Register a DownloadWorkflow as having started for this event.
    Called by DownloadWorkflow at the VERY START of its run.
    Uses $addToSet for idempotency - same workflow ID won't double-count.
    """
    store = MongoStore()
    success = store.add_download_workflow(fixture_id, event_id, workflow_id)
    count = store.get_download_workflow_count(fixture_id, event_id)
    return {"success": success, "count": count}

@activity.defn
async def get_download_workflow_count(
    fixture_id: int, 
    event_id: str
) -> Dict[str, Any]:
    """Get the current count of registered DownloadWorkflows."""
    store = MongoStore()
    count = store.get_download_workflow_count(fixture_id, event_id)
    return {"count": count, "is_complete": count >= 10}

@activity.defn
async def check_and_mark_twitter_complete(
    fixture_id: int, 
    event_id: str
) -> Dict[str, Any]:
    """Check if 10 download workflows are registered and mark complete if so."""
    store = MongoStore()
    count = store.get_download_workflow_count(fixture_id, event_id)
    
    if count >= 10:
        store.mark_twitter_complete(fixture_id, event_id)
        return {"count": count, "marked_complete": True}
    
    return {"count": count, "marked_complete": False}
```

### Activities to Remove/Deprecate

- `increment_twitter_count` - Replaced by `register_download_workflow` + `check_and_mark_twitter_complete`
- `update_event_stable_count` - Replaced by `register_monitor_workflow`
- `confirm_twitter_workflow_started` - Replaced by `set_monitor_complete` (called from Twitter workflow itself)

---

## Sequence Diagrams

### Monitor → Twitter Trigger (New Flow)

```
MonitorWorkflow-1        MongoDB                    MonitorWorkflow-2        MonitorWorkflow-3
     │                      │                              │                       │
     │──add_monitor_wf──────▶│                              │                       │
     │   (workflow_id=1)    │                              │                       │
     │◀──count=1, complete=F│                              │                       │
     │   (< 3, no trigger)  │                              │                       │
     │                      │                              │                       │
     │                      │◀──add_monitor_wf─────────────│                       │
     │                      │   (workflow_id=2)            │                       │
     │                      │──count=2, complete=F─────────▶│                       │
     │                      │   (< 3, no trigger)          │                       │
     │                      │                              │                       │
     │                      │◀──add_monitor_wf───────────────────────────────────────│
     │                      │   (workflow_id=3)                                     │
     │                      │──count=3, complete=F──────────────────────────────────▶│
     │                      │   (>= 3 AND not complete)                             │
     │                      │                                                       │
     │                      │                              TwitterWorkflow STARTS   │
     │                      │◀─────────────────────────────set _monitor_complete=T──│
     │                      │                              (FIRST THING IT DOES)    │
```

### Monitor Spawn Failure Recovery

```
MonitorWorkflow-3        MongoDB                    TwitterWorkflow       MonitorWorkflow-4
     │                      │                              │                    │
     │──add_monitor_wf──────▶│                              │                    │
     │◀──count=3, complete=F│                              │                    │
     │                      │                              │                    │
     │══spawn_twitter═══════╳ (FAILS TO START)            │                    │
     │                      │                              │                    │
     │   (complete still F) │                              │                    │
     │                      │                              │                    │
     │                      │◀──add_monitor_wf─────────────────────────────────│
     │                      │   (workflow_id=4)                                │
     │                      │──count=4, complete=F─────────────────────────────▶│
     │                      │   (>= 3 AND not complete)                        │
     │                      │   RETRY SPAWN!                                   │
     │                      │                                                  │
     │                      │                              TwitterWorkflow     │
     │                      │◀─────────────────────────────set complete=T──────│
```

### Twitter → Download → Upload (New Flow)

```
TwitterWorkflow          MongoDB              DownloadWorkflow         UploadWorkflow
     │                      │                       │                       │
     │──get_download_count──▶│                       │                       │
     │◀──count=0────────────│                       │                       │
     │                      │                       │                       │
     │══start_download_1═══════════════════════════▶│                       │
     │                      │◀──register_download───│ (FIRST THING!)        │
     │                      │   (ID=download1)      │                       │
     │                      │                       │                       │
     │                      │                       │ download & validate   │
     │                      │                       │                       │
     │                      │                       │══signal_upload════════▶│
     │                      │                       │   (even if 0 videos)  │
     │                      │                       │                       │
     │                      │◀──check_complete──────────────────────────────│
     │                      │──count=1, complete=false─────────────────────▶│
     │   sleep(60s)         │                       │                       │
     │                      │                       │                       │
     │──get_download_count──▶│                       │                       │
     │◀──count=1────────────│                       │                       │
     │                      │                       │                       │
     │══start_download_2═══════════════════════════▶│                       │
     │                      │◀──register_download───│                       │
     │          ...         │         ...           │         ...           │
     │                      │                       │                       │
     │   (attempt 10)       │                       │                       │
     │══start_download_10══════════════════════════▶│                       │
     │                      │◀──register_download───│                       │
     │                      │   (ID=download10)     │                       │
     │                      │                       │══signal_upload════════▶│
     │                      │                       │                       │
     │                      │◀──check_complete──────────────────────────────│
     │                      │──count=10, complete=true─────────────────────▶│
     │                      │◀──mark_twitter_complete───────────────────────│
     │                      │                       │                       │
     │──get_download_count──▶│                       │                       │
     │◀──count=10───────────│                       │                       │
     │   (>= 10, EXIT LOOP) │                       │                       │
     ▼                      │                       │                       │
   DONE                     │                       │                       │
                            │                       │                       │
                            │         (5 minutes later, no new signals)    │
                            │                       │                       │
                            │◀──FAILSAFE: check_complete────────────────────│
                            │   (in case batch check failed)               │
                            │──if count=10, mark complete──────────────────▶│
                            │                       │                       ▼
                            │                       │                     DONE
```

### Download Start Failure Scenario

```
TwitterWorkflow          MongoDB              DownloadWorkflow
     │                      │                       │
     │══start_download_5════╳ (FAILS TO START)     │
     │                      │                       │
     │   catch exception    │  (nothing registered) │
     │                      │                       │
     │   sleep(60s)         │                       │
     │                      │                       │
     │──get_download_count──▶│                       │
     │◀──count=4────────────│  (still only 4!)     │
     │                      │                       │
     │   (count < 10, try again - this is attempt 6 but only 4 registered)
     │                      │                       │
     │══start_download_6═══════════════════════════▶│
     │                      │◀──register_download───│ (now count=5)
     │          ...         │                       │
```

The `while count < 10` loop naturally handles start failures by retrying until 10 workflows actually run.

---

## Benefits Summary

1. **Eliminates the "infinite workflow" bug** - Can't lose a count if we're storing workflow IDs
2. **Full audit trail** - See exactly which workflows ran for any event
3. **Idempotent** - `$addToSet` means re-running a workflow doesn't double-count
4. **Atomic** - No race conditions between workflows checking/incrementing counters
5. **Debuggable** - "Why is this event stuck?" → Check the array, find the missing workflow
6. **Simpler code paths** - Remove conditional "increment ourselves" branches
7. **Consistent pattern** - Same approach for Monitor (3 workflows) and Download (10 workflows)
8. **Failsafe completion** - UploadWorkflow checks completion on exit, catching any missed batch checks

---

## Drawbacks / Considerations

1. **Slightly larger documents** - Storing 10 workflow ID strings vs a single integer
   - Workflow IDs are ~50-80 chars each
   - Max overhead: ~800 bytes per event (negligible)

2. **More MongoDB reads** - `get_download_workflow_count` queries before each attempt
   - But we're already doing reads anyway for `check_event_exists`
   - Could optimize by returning count in other activity responses

3. **Migration** - Old events have `_monitor_count`/`_twitter_count`, not arrays
   - Solution: Don't migrate. Deploy fresh. In-flight events will fail and that's fine.

4. **Workflow ID format dependency** - If we change ID format, old IDs in arrays still work
   - Not really a problem, just something to be aware of

---

## Implementation Checklist

### Phase 1: Model Updates ✅
- [x] Add `MONITOR_WORKFLOWS` and `DOWNLOAD_WORKFLOWS` to `EventFields` in `models.py`
- [x] Remove `MONITOR_COUNT` and `TWITTER_COUNT` from `EventFields` (or deprecate) - DEPRECATED
- [x] Include `initial_monitor_workflows` param in `create_new_enhanced_event()` - **BUG FIX: The discovering workflow must be added to the array**

### Phase 2: MongoDB Store Functions ✅
- [x] Add `add_monitor_workflow()` in `mongo_store.py`
- [x] Add `add_download_workflow()` in `mongo_store.py`
- [x] Add `get_monitor_workflow_count()` in `mongo_store.py`
- [x] Add `get_download_workflow_count()` in `mongo_store.py`
- [x] Add `set_monitor_complete()` in `mongo_store.py` - named `mark_monitor_complete()`
- [x] Add `mark_twitter_complete()` in `mongo_store.py`
- [x] Add `check_and_mark_twitter_complete()` in `mongo_store.py`

### Phase 3: Activities ✅
- [x] Add `register_monitor_workflow` activity in `monitor.py`
- [x] Add `set_monitor_complete` activity in `twitter.py`
- [x] Add `register_download_workflow` activity in `download.py`
- [x] Add `get_download_workflow_count` activity in `twitter.py`
- [x] Add `check_and_mark_twitter_complete` activity in `download.py`
- [x] Remove/deprecate `increment_twitter_count` activity - DEPRECATED

### Phase 4: Workflow Updates ✅

**MonitorWorkflow:** ✅
- [x] Pass workflow ID to `process_fixture_events`
- [x] Update `process_fixture_events` to use `register_monitor_workflow`
- [x] Update trigger logic: if `len(_monitor_workflows) >= 3 AND _monitor_complete = false` → start Twitter
- [x] Remove any logic that sets `_monitor_complete` (Twitter does this now)

**TwitterWorkflow:** ✅
- [x] Add `set_monitor_complete(true)` at the VERY START (first thing after workflow starts)
- [x] Replace `for attempt in range(1, 11)` with `while` loop
- [x] Add `get_download_workflow_count` check at start of each iteration
- [x] Exit loop when count >= 10
- [x] Add max attempts safety limit (e.g., 15) to prevent infinite loops
- [x] ALWAYS start DownloadWorkflow (even with 0 videos found)
- [x] Remove all `increment_twitter_count` calls
- [x] Let start failures naturally retry via the while loop

**DownloadWorkflow:** ✅
- [x] Add `register_download_workflow` at the VERY START (first thing after workflow starts)
- [x] ALWAYS signal UploadWorkflow (even with empty video list)
- [x] Remove conditional `increment_twitter_count` branch
- [x] Remove `_increment_twitter_count` helper method

**UploadWorkflow:** ✅
- [x] Add `check_and_mark_twitter_complete` after each batch
- [x] Add failsafe `check_and_mark_twitter_complete` before idle timeout exit
- [x] Remove `_increment_twitter_count` calls - replaced with `_check_and_mark_twitter_complete`

### Phase 5: Cleanup ✅
- [x] Remove old counter increment code paths - DEPRECATED with comments
- [x] Update logging to reference new fields (`_download_workflows` instead of `_twitter_count`)
- [ ] Simplify/remove CASE 2 "stuck event" logic in `process_fixture_events` - Kept for safety
- [x] Update documentation (ARCHITECTURE.md, TEMPORAL_WORKFLOWS.md, ORCHESTRATION.md)

### Phase 6: VAR Removal Refactor (`_drop_workflows`)
- [ ] Add `DROP_WORKFLOWS` field to `EventFields` in `models.py`
- [ ] Add `_drop_workflows: []` to `create_new_enhanced_event()` initial state
- [ ] Add `add_drop_workflow()` in `mongo_store.py` - Uses `$addToSet`
- [ ] Add `clear_drop_workflows()` in `mongo_store.py` - Sets `_drop_workflows: []`
- [ ] Add `get_drop_workflow_count()` in `mongo_store.py`
- [ ] Update `process_fixture_events` REMOVED events logic:
  - [ ] When event SEEN (MATCHING): Call `clear_drop_workflows()` to reset
  - [ ] When event MISSING (REMOVED): Call `add_drop_workflow(workflow_id)`
  - [ ] When `len(_drop_workflows) >= 3`: Delete event (and S3 if completed)
- [ ] Remove old `_monitor_count` decrement logic

### Phase 7: Testing
- [ ] Deploy to dev environment
- [ ] Monitor today's fixtures
- [ ] Verify events complete properly with new tracking
- [ ] Check MongoDB documents have workflow ID arrays populated
- [ ] Test edge cases: 0-video attempts, workflow failures, VAR'd events
- [ ] Deploy to production (next day)

---

## Related Files

| File | Changes |
|------|---------|
| `src/data/models.py` | Add new EventFields constants |
| `src/data/mongo_store.py` | Add workflow array helper functions, add `mark_monitor_complete` |
| `src/activities/monitor.py` | Add `register_monitor_workflow` activity, update `process_fixture_events` trigger logic |
| `src/activities/twitter.py` | Add `set_monitor_complete` activity |
| `src/activities/download.py` | Add `register_download_workflow`, `get_download_workflow_count`, `check_and_mark_twitter_complete` |
| `src/workflows/monitor_workflow.py` | Pass workflow ID to activities, check `_monitor_complete` before spawning Twitter |
| `src/workflows/twitter_workflow.py` | Add `set_monitor_complete` at START, change for→while loop, always call download |
| `src/workflows/download_workflow.py` | Add `register_download_workflow` at START, always signal upload |
| `src/workflows/upload_workflow.py` | Check count, mark complete, add failsafe on exit |

---

## Potential Issues & Considerations

### 1. DownloadWorkflow Fails to Start

**Scenario:** TwitterWorkflow tries to start DownloadWorkflow, but it fails (Temporal issue, task queue full, etc.)

**Current behavior:** TwitterWorkflow catches the exception and calls `increment_twitter_count` itself.

**New behavior:** If DownloadWorkflow never starts, it never registers itself. The count stays at N-1.

**Why this is fine:** The `while count < 10` loop in TwitterWorkflow naturally handles this:
- Start attempt fails → workflow doesn't register → count unchanged
- Loop continues → sleep 60s → check count again → still < 10 → try again
- We keep trying until 10 workflows actually start and register
- Max attempts safety limit (15) prevents infinite loops if something is seriously broken

This correctly reflects reality: we want 10 workflows to *actually run*, not just 10 attempts.

### 2. UploadWorkflow Already Completed

**Scenario:** DownloadWorkflow 10 finishes, UploadWorkflow marks complete and exits. Then a slow DownloadWorkflow 9 tries to signal it.

**Current behavior:** Signal-with-start would START a new UploadWorkflow (since ID is now available).

**New behavior:** Same - a new UploadWorkflow starts, processes the signal, checks count (already 10), and exits.

**Not a problem:** The completion check is idempotent. Marking `_twitter_complete=true` when it's already true is a no-op.

### 3. Race: Two DownloadWorkflows Register "Simultaneously"

**Scenario:** Download 9 and Download 10 both call `register_download_workflow` at nearly the same time.

**MongoDB behavior:** `$addToSet` is atomic per document. Both will succeed, array will have both IDs.

**Count check:** After each registers, they both signal UploadWorkflow. UploadWorkflow processes signals sequentially (FIFO). One of them will see count=10 first and mark complete.

**Not a problem:** Array operations are atomic, and the serialization happens in UploadWorkflow.

### 4. MonitorWorkflow ID Uniqueness

**Current MonitorWorkflow ID format:** `monitor-{DD_MM_YYYY}-{HH:MM}`

**Issue:** Multiple MonitorWorkflows can have the same ID if they run at the same minute. This is intentional for preventing duplicates via Temporal's workflow ID uniqueness.

**Impact on new approach:** If the same MonitorWorkflow ID processes the same event twice (shouldn't happen), `$addToSet` prevents double-counting.

**Consideration:** Do we want to add a unique suffix (run_id) to distinguish workflow instances? Probably not - the current ID format is intentional.

### 5. VAR'd Events

**Scenario:** Event is VAR'd after Download 5 has run. The array has 5 workflow IDs, but no more will be added.

**Current behavior:** Events that are VAR'd have `_removed: true` set. Workflows check `check_event_exists` and exit early.

**New behavior:** Same - but the array will have < 10 entries. `_twitter_complete` won't be set.

**Is this a problem?** No - VAR'd events shouldn't be marked complete. The `_removed` flag handles this separately.

**Consideration:** Should we mark `_twitter_complete=true` for VAR'd events to prevent them from appearing as "incomplete"? This is existing behavior - the VAR'd event completion is a separate concern.

### 6. TwitterWorkflow While Loop Termination

**Proposed change:** `for attempt in range(1, 11)` → `while len(_download_workflows) < 10`

**Concern:** What if `get_download_workflow_count` fails repeatedly? Infinite loop?

**Solution:** Add a max attempts safeguard:

```python
attempt = 0
max_attempts = 15  # Safety limit (should only need 10)

while True:
    attempt += 1
    if attempt > max_attempts:
        workflow.logger.error("Exceeded max attempts safety limit")
        break
    
    count = await get_download_workflow_count(...)
    if count >= 10:
        break
    
    # ... rest of the logic
```

### 7. Empty Batch Signaling Cost

**Change:** DownloadWorkflow ALWAYS signals UploadWorkflow, even with 0 videos.

**Impact:** 
- More signals to UploadWorkflow (up to 10 per event, regardless of video count)
- Each signal = MongoDB read to check count
- UploadWorkflow stays alive longer (signals keep coming)

**Is this acceptable?** Yes - we already handle empty batches, and the overhead is minimal (one MongoDB read per signal). The benefit (guaranteed completion tracking) outweighs the cost.

### 8. Existing Workarounds to Remove

After implementing this refactor, we can simplify/remove:

1. **`check_twitter_workflow_running`** - May still be useful for debugging, but not critical for logic
2. **CASE 2 in process_fixture_events** - "Stuck event" detection (count=3, monitor_complete=false) - replaced by array-based tracking
3. **Conditional increment in TwitterWorkflow** - No longer needed (DownloadWorkflow always signals UploadWorkflow)
4. **Conditional increment in DownloadWorkflow** - No longer needed (always signals UploadWorkflow)
5. **`increment_twitter_count` activity** - Replaced entirely by `check_and_mark_twitter_complete`

### 9. Logging Changes Needed

Update log messages to reference new fields:

| Old Log | New Log |
|---------|---------|
| `_monitor_count=3` | `_monitor_workflows=3` |
| `_twitter_count=7` | `_download_workflows=7` |
| `increment_twitter_count` | `check_and_mark_twitter_complete` |

### 10. Frontend/API Impact

**If the frontend reads `_monitor_count` or `_twitter_count`:**
- Need to update to read array length instead
- Or add computed fields: `monitor_workflow_count`, `download_workflow_count`

**Check:** Does the frontend/API use these fields directly? If so, consider backward compatibility.

---

## Testing Checklist

Before deploying to production:

- [ ] Single event flow: Monitor → Twitter → 10 Downloads → Upload → Complete
- [ ] Event with 0 videos found on some attempts (empty signal handling)
- [ ] Event with all 10 attempts finding 0 videos
- [ ] Multiple events in the same fixture (parallel processing)
- [ ] VAR'd event mid-workflow (graceful termination)
- [ ] Workflow restart/retry scenarios
- [ ] Check MongoDB documents have correct array contents
- [ ] Verify `_twitter_complete` is set correctly at count=10
- [ ] Verify failsafe triggers on UploadWorkflow idle timeout |
