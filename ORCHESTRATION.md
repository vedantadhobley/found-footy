# Found Footy - Orchestration Model

## 🎯 Core Principle: Monitor is the Single Orchestrator

The **MonitorWorkflow** is the central orchestrator for all event processing. It runs every minute and manages the entire lifecycle of each event through counter-based tracking.

---

## 📊 Event State Machine

Each event goes through a simple state machine controlled by the Monitor:

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                         MONITOR ORCHESTRATION                                │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    PHASE 1: DEBOUNCE (Monitor Count)                  │   │
│  │                                                                        │   │
│  │   _monitor_complete = FALSE                                            │   │
│  │                                                                        │   │
│  │   Each minute:                                                         │   │
│  │     IF _monitor_count < 3:  increment count                            │   │
│  │     IF _monitor_count >= 3: set _monitor_complete = TRUE               │   │
│  │                              set _twitter_count = 1                     │   │
│  │                              trigger TwitterWorkflow                    │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                              │                                               │
│                              ▼ (_monitor_complete = TRUE)                    │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    PHASE 2: TWITTER (Twitter Count)                   │   │
│  │                                                                        │   │
│  │   _twitter_complete = FALSE                                            │   │
│  │                                                                        │   │
│  │   Each minute:                                                         │   │
│  │     IF NOT _twitter_complete:                                          │   │
│  │       IF _twitter_count < 3:  increment count                          │   │
│  │                                trigger TwitterWorkflow                  │   │
│  │                                                                        │   │
│  │   TwitterWorkflow (when done):                                         │   │
│  │     sets _twitter_complete = TRUE                                      │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                              │                                               │
│                              ▼ (_twitter_complete = TRUE)                    │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         PHASE 3: COMPLETE                             │   │
│  │                                                                        │   │
│  │   When ALL events have:                                                │   │
│  │     _monitor_complete = TRUE  AND                                      │   │
│  │     _twitter_complete = TRUE                                           │   │
│  │                                                                        │   │
│  │   → Fixture moves to fixtures_completed                                │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                                                                              │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## 🔢 Event Tracking Fields

| Field | Set By | Meaning |
|-------|--------|---------|
| `_monitor_count` | Monitor | Number of consecutive debounce cycles (1, 2, 3) |
| `_monitor_complete` | Monitor | TRUE when debounce finished (count reached 3) |
| `_twitter_count` | Monitor | Number of Twitter attempts started (1, 2, 3) |
| `_twitter_complete` | Twitter Workflow | TRUE when Twitter workflow finishes (including downloads) |

---

## 🔄 Monitor Decision Tree

```python
for each event in fixture:
    
    if NOT event._monitor_complete:
        # PHASE 1: Still debouncing
        if event._monitor_count < 3:
            event._monitor_count += 1
            # Event still stabilizing...
        
        if event._monitor_count >= 3:
            event._monitor_complete = True
            event._twitter_count = 1
            trigger TwitterWorkflow(attempt=1)
    
    else:  # _monitor_complete = True
        # PHASE 2: Check Twitter status
        if NOT event._twitter_complete:
            if event._twitter_count < 3:
                event._twitter_count += 1
                trigger TwitterWorkflow(attempt=twitter_count)
            # else: waiting for last Twitter workflow to finish
```

---

## 📋 Workflow Responsibilities

### MonitorWorkflow (Orchestrator)
- Runs every minute
- Tracks `_monitor_count` and `_monitor_complete`
- Tracks `_twitter_count` (increments BEFORE triggering Twitter)
- Triggers TwitterWorkflow when appropriate
- Checks if fixture can be completed

### TwitterWorkflow (Worker)
- Does the actual Twitter search
- Triggers DownloadWorkflow as child
- Sets `_twitter_complete = TRUE` when done (in finally block)
- This is the signal that all work for this attempt is finished

### DownloadWorkflow (Worker)
- Downloads videos from Twitter URLs
- Uploads to S3
- Saves results to MongoDB
- Called by TwitterWorkflow, not directly by Monitor

---

## 🏁 Fixture Completion Logic

A fixture can only be completed when:

1. **ALL valid events** have `_monitor_complete = TRUE`
2. **ALL valid events** have `_twitter_complete = TRUE`

This ensures:
- All debouncing is finished
- All Twitter searches have completed
- All downloads have finished

```python
def complete_fixture(fixture_id):
    valid_events = [e for e in events if not e._removed and e._event_id]
    
    all_monitored = all(e._monitor_complete for e in valid_events)
    all_twitter_done = all(e._twitter_complete for e in valid_events)
    
    if all_monitored and all_twitter_done:
        move_to_completed(fixture_id)
```

---

## ⏱️ Timeline Example

```
Minute 0:  Goal scored! Event appears in API
Minute 1:  Monitor sees new event → _monitor_count = 1
Minute 2:  Monitor sees event again → _monitor_count = 2
Minute 3:  Monitor sees event again → _monitor_count = 3 → _monitor_complete = TRUE
           → Triggers TwitterWorkflow(attempt=1) → _twitter_count = 1
           
           TwitterWorkflow runs (60-150 seconds)
           → Downloads videos
           → Sets _twitter_complete = TRUE (when done)
           
Minute 4:  Monitor checks: _twitter_complete = TRUE for all events?
           If yes AND fixture FT → complete_fixture()
           If no → keep waiting
           
           OR if _twitter_complete = FALSE and _twitter_count < 3:
           → Triggers TwitterWorkflow(attempt=2) → _twitter_count = 2
           
...repeat until _twitter_count = 3 and all workflows finish...
```

---

## 🎯 Key Design Decisions

### Why Monitor tracks count, Twitter sets complete?

1. **Clear separation of concerns**
   - Monitor knows "how many attempts have I started"
   - Twitter knows "have I finished my work"

2. **Race condition prevention**
   - If Monitor set `_twitter_complete`, it would happen BEFORE Twitter finishes
   - By having Twitter set it, we know downloads are actually done

3. **Simple state management**
   - Monitor only increments counters
   - Twitter only sets completion flag
   - No complex coordination needed

### Why non-blocking child workflows?

TwitterWorkflow uses `ParentClosePolicy.ABANDON` so:
- Monitor doesn't block waiting for searches
- Multiple Twitter searches can run in parallel
- Monitor can continue processing other fixtures

The `_twitter_complete` flag ensures we still track when work is done.

---

## 🚨 Error Handling

### Twitter workflow fails
- `_twitter_complete` is set in `finally` block
- Even if search/download fails, completion flag is set
- Fixture can still complete (with partial or no videos)

### Event removed (VAR disallowed)
- Event marked `_removed = TRUE`
- Ignored in completion checks
- Fixture can complete without waiting for removed events
