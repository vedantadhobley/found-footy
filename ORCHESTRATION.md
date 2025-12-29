# Found Footy - Orchestration Model

## 🎯 Core Principle: Decoupled Workflow Architecture

The system uses a **decoupled architecture** where:
- **MonitorWorkflow** handles debouncing and triggers RAGWorkflow **ONCE** per event
- **RAGWorkflow** resolves team aliases (stub now, LLM later) and triggers TwitterWorkflow
- **TwitterWorkflow** manages all 3 search attempts **internally** with 3-minute durable timers

This decoupling allows Twitter searches to run at 3-minute intervals instead of being tied to Monitor's 1-minute poll cycle.

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
│  │   Each minute (Monitor poll):                                          │   │
│  │     IF event seen: increment _monitor_count                            │   │
│  │     IF _monitor_count >= 3:                                            │   │
│  │       → set _monitor_complete = TRUE                                   │   │
│  │       → set _twitter_count = 1 (initial)                               │   │
│  │       → trigger RAGWorkflow (ONCE, fire-and-forget)                    │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                              │                                               │
│                              ▼ (RAGWorkflow started)                         │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                    PHASE 2: TWITTER (Self-Managed)                    │   │
│  │                                                                        │   │
│  │   RAGWorkflow:                                                         │   │
│  │     1. get_team_aliases(team_name) → ["Liverpool", "LFC", "Reds"]      │   │
│  │     2. save_team_aliases to MongoDB                                    │   │
│  │     3. Start TwitterWorkflow (child, waits for completion)             │   │
│  │                                                                        │   │
│  │   TwitterWorkflow (manages all 3 attempts internally):                 │   │
│  │     FOR attempt IN [1, 2, 3]:                                          │   │
│  │       → update_twitter_attempt(attempt)                                │   │
│  │       → Search all aliases: "Salah Liverpool", "Salah LFC", ...        │   │
│  │       → Dedupe videos, save to _discovered_videos                      │   │
│  │       → Trigger DownloadWorkflow                                       │   │
│  │       → IF attempt < 3: sleep(3 minutes) ← DURABLE TIMER               │   │
│  │     AFTER attempt 3:                                                   │   │
│  │       → set _twitter_complete = TRUE                                   │   │
│  │                                                                        │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
│                              │                                               │
│                              ▼ (_twitter_complete = TRUE)                    │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                         PHASE 3: COMPLETE                             │   │
│  │                                                                        │   │
│  │   When fixture status = FT/AET/PEN AND:                                │   │
│  │     ALL events have _monitor_complete = TRUE                           │   │
│  │     ALL events have _twitter_complete = TRUE                           │   │
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
| `_monitor_count` | Monitor | Each poll when event seen | Debounce counter (1, 2, 3) |
| `_monitor_complete` | Monitor | When `_monitor_count >= 3` | Debounce finished |
| `_twitter_aliases` | RAGWorkflow | After alias lookup | Team search variations |
| `_twitter_count` | TwitterWorkflow | Start of each attempt | Tracks current attempt (1, 2, 3) |
| `_twitter_complete` | TwitterWorkflow | After attempt 3 | All searches finished |

---

## 🔄 Workflow Responsibilities

### MonitorWorkflow (Scheduled Every Minute)
- Polls active fixtures from API
- Increments `_monitor_count` for seen events
- Sets `_monitor_complete = TRUE` when count reaches 3
- **Triggers RAGWorkflow ONCE** (fire-and-forget)
- Checks fixture completion eligibility
- **Does NOT manage Twitter retries** (that's TwitterWorkflow's job now)

### RAGWorkflow (Triggered by Monitor)
- **Checks cache first** - aliases pre-cached during ingestion
- If cache miss: runs full Wikidata + LLM RAG pipeline
- Determines team type via API-Football (`team.national` boolean)
- Saves aliases to `_twitter_aliases` in MongoDB
- Triggers TwitterWorkflow as child (waits for completion)

### TwitterWorkflow (Triggered by RAGWorkflow)
- **Self-manages all 3 attempts** with durable timers
- Builds search queries: `{player_last} {alias}` for each alias
- Deduplicates videos across aliases and previous attempts
- Triggers DownloadWorkflow after each attempt
- Updates `_twitter_count` at start of each attempt
- Sets `_twitter_complete = TRUE` after attempt 3

### DownloadWorkflow (Triggered by TwitterWorkflow)
- Downloads videos from Twitter URLs
- Applies duration filter (>3s to 60s)
- Validates soccer content via vision model (Qwen3-VL)
- Computes perceptual hash for deduplication (dense 0.25s sampling)
- Compares with existing S3 videos (requires 3 consecutive frames to match)
- Uploads new/better videos to S3

---

## ⏱️ Timeline Example

```
T+0:00  Goal scored! Event appears in API
T+1:00  Monitor poll #1 → _monitor_count = 1
T+2:00  Monitor poll #2 → _monitor_count = 2
T+3:00  Monitor poll #3 → _monitor_count = 3
        → _monitor_complete = TRUE
        → RAGWorkflow triggered (fire-and-forget)
        
T+3:05  RAGWorkflow:
        → get_cached_team_aliases(40) → ["LFC", "Reds", "Anfield", "Liverpool"]
        → save to _twitter_aliases in event
        → Start TwitterWorkflow
        
T+3:10  TwitterWorkflow Attempt 1:
        → _twitter_count = 1
        → Search "Salah LFC" → 3 videos
        → Search "Salah Reds" → 2 videos (1 dup)
        → Search "Salah Anfield" → 1 video (all dups)
        → Search "Salah Liverpool" → 1 video
        → Dedupe → 5 unique videos
        → DownloadWorkflow → 4 uploaded to S3
        → Sleep until next 3-min boundary (~T+6:00)
        
T+6:00  TwitterWorkflow Attempt 2:
        → _twitter_count = 2
        → Same 4 searches (new videos may exist)
        → 1 new video found
        → DownloadWorkflow → 1 uploaded
        → Sleep until ~T+9:00
        
T+9:00  TwitterWorkflow Attempt 3:
        → _twitter_count = 3
        → Same 4 searches
        → 0 new videos
        → _twitter_complete = TRUE
        
T+10:00 Monitor sees:
        → Fixture status = FT
        → All events: _monitor_complete = TRUE
        → All events: _twitter_complete = TRUE
        → Move fixture to fixtures_completed
```

---

## 🎯 Key Design Decisions

### Why decouple Twitter from Monitor?

**Before**: Monitor triggered TwitterWorkflow on each 1-minute poll
- 3 attempts at ~1-minute intervals (tied to poll cycle)
- Monitor logic was complex (tracking `twitter_retry_needed`)

**After**: Monitor triggers RAGWorkflow ONCE, TwitterWorkflow self-manages
- 3 attempts at 3-minute intervals (better for video discovery)
- Monitor logic is simple (just trigger once)
- Durable timers survive worker restarts

### Why 3-minute spacing?

Goal videos appear on Twitter over 5-15 minutes:
- Attempt 1 (immediately): Catch early uploads
- Attempt 2 (+3 min): New uploads appearing
- Attempt 3 (+6 min): Final sweep for late/HD uploads

### Why RAGWorkflow as intermediary?

1. **Clean separation**: Alias lookup is separate from Twitter search
2. **Future extensibility**: Wikidata + LLM implementation is isolated
3. **Visibility**: Aliases saved to MongoDB for debugging
4. **Pre-caching**: Aliases cached during ingestion, fast lookup at runtime

### Why pre-cache aliases during ingestion?

During daily ingest (00:05 UTC), we pre-cache RAG aliases for BOTH teams in every fixture:
- Ensures aliases are ready before any goals are scored
- Covers opponent teams (non-tracked teams that play against our tracked teams)
- Cache lookup is O(1) by team_id at runtime
- Full RAG pipeline (Wikidata + LLM) only runs once per team

### Why use API-Football for team type?

Team type (club vs national) determines alias generation:
- National teams get nationality adjectives ("Belgian", "French", "Brazilian")
- Clubs get standard nickname processing

We use API-Football's `team.national` boolean because:
- **Authoritative**: API knows definitively if a team is national
- **No heuristics**: Don't guess based on team_id ranges
- **Works for all teams**: Mali (ID 1500) is correctly identified as national

### Why durable timers?

Temporal's `workflow.sleep()` survives:
- Worker restarts
- Container crashes
- Network issues

The 3-minute wait is guaranteed even if the worker dies mid-wait.

---

## 🏁 Fixture Completion Logic

A fixture moves to `fixtures_completed` when:

1. **Fixture status** is `FT`, `AET`, or `PEN`
2. **ALL valid events** have `_monitor_complete = TRUE`
3. **ALL valid events** have `_twitter_complete = TRUE`

```python
def complete_fixture_if_ready(fixture_id):
    fixture = get_fixture(fixture_id)
    valid_events = [e for e in events if not e._removed and e._event_id]
    
    all_monitored = all(e._monitor_complete for e in valid_events)
    all_twitter_done = all(e._twitter_complete for e in valid_events)
    
    if all_monitored and all_twitter_done:
        move_to_completed(fixture_id)
```

---

## 🚨 Error Handling

### TwitterWorkflow fails mid-execution
- Temporal retries the workflow
- `_twitter_count` shows how many attempts started
- Partial videos already downloaded are preserved

### Event removed (VAR disallowed)
- Event marked `_removed = TRUE` by Monitor
- Ignored in completion checks
- Any running TwitterWorkflow continues but results are orphaned

### LLM unavailable
- RAGWorkflow activity falls back to `[team_name]`
- Search still works, just with single alias
- Video validation uses fail-closed: skip video if AI unavailable
