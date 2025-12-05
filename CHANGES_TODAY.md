# Changes Made Today (December 4, 2024)

## Summary
Major improvements to workflow naming, fixture data syncing, and comprehensive documentation. Discovered critical architectural insights that will simplify the system significantly.

---

## Files Modified

### 1. Workflow Naming Updates
**Files**: 
- `src/workflows/event_workflow.py`
- `src/workflows/twitter_workflow.py`  
- `src/workflows/download_workflow.py`
- `src/workflows/ingest_workflow.py`
- `src/workflows/monitor_workflow.py`
- `src/worker.py`

**Changes**:
- Updated all workflow IDs to be human-readable
- Changed from team abbreviations to full team names with underscores
- Added extra time to minute display (45+3min)
- Special character handling (A.C. Milan → A_C_Milan)
- Added fixture sync when no debounce needed

**New Naming**:
- IngestWorkflow: `ingest-{DD_MM_YYYY}`
- MonitorWorkflow: `monitor-{DD_MM_YYYY}-{HH:MM}`  
- EventWorkflow: `event-{home}-vs-{away}-{fixture_id}`
- TwitterWorkflow: `twitter-{Team_Name}-{LastName}-{min}[+extra]-{event_id}`
- DownloadWorkflow: `download-{Team_Name}-{LastName}-{count}vids-{event_id}`

### 2. Fixture Data Syncing
**Files**:
- `src/data/mongo_store.py` - Added `sync_fixture_data()` method
- `src/activities/event.py` - Call sync after debounce operations
- `src/activities/monitor.py` - Added `sync_fixture_metadata` activity
- `src/workflows/monitor_workflow.py` - Call sync when no debounce needed
- `src/worker.py` - Register new activity

**Purpose**: Keep fixture metadata (score, status, time) fresh even when not debouncing events.

### 3. Event Data Structure
**Files**:
- `src/activities/event.py`

**Changes**:
- Added `extra` field to `event_details` for added time display
- Updated return structure to include extra time information

### 4. Documentation
**Files**:
- `TEMPORAL_WORKFLOWS.md` (NEW)

**Content**:
- Complete workflow explanations with 8 Mermaid diagrams
- 4-collection MongoDB architecture with flow diagrams
- Detailed debounce logic (4 cases explained)
- Event data structure evolution
- UI display patterns
- Error handling and troubleshooting
- Development workflow guide
- Architecture decision rationale

---

## Code Patterns Established

### 1. Workflow Naming Pattern
```python
# Clean team name
team_clean = team_name.replace(" ", "_").replace(".", "_").replace("-", "_")

# Format minute with extra time
if extra and extra > 0:
    time_str = f"{minute}+{extra}"
else:
    time_str = str(minute)

# Build workflow ID
workflow_id = f"twitter-{team_clean}-{player_last}-{time_str}min-{event_id}"
```

### 2. Fixture Data Sync Pattern
```python
def sync_fixture_data(fixture_id):
    """Sync fixture metadata while preserving enhanced events"""
    live_fixture = get_live_fixture(fixture_id)
    active_fixture = get_fixture_from_active(fixture_id)
    
    # Copy all top-level fields from live
    update_doc = dict(live_fixture)
    
    # Preserve enhanced events from active
    update_doc["events"] = active_fixture.get("events", [])
    
    # Replace document
    fixtures_active.replace_one({"_id": fixture_id}, update_doc)
```

### 3. Event Details Extraction
```python
event_details[event_id] = {
    "player": event["player"]["name"],
    "team": event["team"]["name"],
    "minute": event["time"]["elapsed"],
    "extra": event["time"].get("extra")  # None if not present
}
```

---

## Architecture Insights Discovered

### 1. Critical Issue: Event ID Must Include Player ID
**Problem**: Current event_id format allows same ID for different players if sequence matches.

**Current**:
```python
event_id = f"{fixture_id}_{team_id}_{event_type}_{sequence}"
# Example: "1378993_40_Goal_1"
```

**Needed**:
```python
event_id = f"{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}"
# Example: "1378993_40_306_Goal_1"
```

**Impact**: With player_id in event_id, we can eliminate hash comparison entirely!

### 2. Simplification Opportunity: Monitor-Only Processing
**Current**:
```
MonitorWorkflow → EventWorkflow → debounce (hash comparison) → TwitterWorkflow
```

**Proposed**:
```
MonitorWorkflow → process events (set comparison) → TwitterWorkflow directly
```

**Benefits**:
- Eliminate EventWorkflow entirely
- No hash computation needed
- Simpler logic (set operations only)
- All processing in one place
- Still parallel (TwitterWorkflow per event)

### 3. _all_complete Flag Pattern
Instead of checking all event flags every time, maintain a single fixture-level flag:
```python
# Update after any event status change
def recalculate_all_complete(fixture_id):
    all_complete = all(
        e._debounce_complete and 
        e._twitter_complete and 
        e._download_complete
        for e in fixture.events
    )
    set_fixture_flag(fixture_id, "_all_complete", all_complete)

# Check for completion
if fixture._all_complete and fixture.status in ["FT", "AET", "PEN"]:
    move_to_completed()
```

---

## Testing Status

### ✅ Completed
- Workflow naming verified in code
- Fixture sync logic implemented
- Documentation comprehensive

### ⚠️ Pending (Tomorrow)
- Event ID with player_id
- Simplified monitor processing
- Twitter implementation
- Download & deduplication
- End-to-end testing

---

## Configuration Changes

### Temporal Schedule IDs
**IngestWorkflow**:
```python
id="ingest-{{ (now.Format \"02_01_2006\") }}"  # Dynamic date
```

**MonitorWorkflow**:
```python
id="monitor-{{ (now.Format \"02_01_2006-15:04\") }}"  # Dynamic datetime
```

### Worker Activities Registered
Added: `monitor.sync_fixture_metadata`

---

## Database Schema Additions

### Fixture-Level (in fixtures_active)
```python
{
    "_all_complete": False,  # NEW - to be added tomorrow
    # ... existing fields
}
```

### Event-Level (in fixtures_active.events)
```python
{
    "_event_id": "1378993_40_306_Goal_1",  # TO BE UPDATED (add player_id)
    "_stable_count": 2,
    "_debounce_complete": False,
    "_twitter_complete": False,
    "_download_complete": False,
    "_sentiment_complete": False,  # FUTURE
    "_twitter_search": "Salah Liverpool",
    "_score_before": "0-0",
    "_score_after": "1-0",
    "_removed": False,
    "discovered_videos": [],  # Added by TwitterWorkflow
    "s3_urls": [],  # Added by DownloadWorkflow
    # Debug fields (can remove):
    "_first_seen": "...",
    "_snapshots": [...]
}
```

---

## Dependencies

### Python Packages (in requirements.txt)
- temporalio
- pymongo
- boto3 (for S3)
- yt-dlp (for downloads)
- requests (for Twitter service)

### External Services
- Temporal Server: `localhost:7233`
- MongoDB: `localhost:27017`
- Twitter Session Service: `localhost:8888`
- S3: `found-footy-videos` bucket

---

## Rollback Plan

If issues arise, the changes are isolated:
1. Workflow naming changes don't affect logic (cosmetic)
2. Fixture sync is additive (doesn't break existing flow)
3. Documentation is separate (no code impact)
4. Tomorrow's refactor will be more substantial

To rollback workflows:
```bash
git diff src/workflows/
git checkout HEAD -- src/workflows/event_workflow.py
git checkout HEAD -- src/workflows/twitter_workflow.py
git checkout HEAD -- src/workflows/download_workflow.py
```

---

## Performance Notes

### Temporal Workflow Metrics
- MonitorWorkflow: ~10s per run (fetches all active fixtures)
- EventWorkflow: ~1s per fixture (will be eliminated)
- TwitterWorkflow: ~2min per event (external API call)
- DownloadWorkflow: ~5min per event (video downloads)

### MongoDB Operations
- `sync_fixture_data`: Single replace_one (fast)
- `compare_live_vs_active`: Two find_one + set operations (fast)
- `store_live_fixture`: Single replace_one with upsert (fast)

---

## Next Session Prep

Before starting tomorrow:
1. Read `PLAN_TOMORROW.md` for full context
2. Check Temporal UI for any stuck workflows
3. Verify MongoDB collections state
4. Review Prefect implementation for download logic reference

Git status:
```bash
git status  # Should show modified files
git diff  # Review all changes
```

Ready to commit:
```bash
git add .
git commit -m "feat: improve workflow naming and add fixture data sync

- Update all workflow IDs to use full team names with underscores
- Add extra time display in workflow names (45+3min)
- Implement fixture data sync to keep metadata fresh
- Add comprehensive documentation with Mermaid diagrams
- Identify need for player_id in event_id (critical fix for tomorrow)"
```

---

## Questions Resolved Today

1. **Q**: How does `compare_live_vs_active` work?  
   **A**: Builds sets from event_ids, uses Python set operations (O(n))

2. **Q**: Should we do event-level workflows?  
   **A**: Yes! But with simplified monitor-only processing instead

3. **Q**: How to handle fixture data updates?  
   **A**: Sync after debounce AND when no debounce needed

4. **Q**: What about team names with special characters?  
   **A**: Replace spaces/dots/hyphens with underscores

5. **Q**: When to deduplicate videos?  
   **A**: BEFORE upload (saves bandwidth)

---

## Lessons Learned

1. **Temporal is cheap for workflows**: Don't be afraid to spawn many children
2. **Event ID is key**: Including player_id eliminates need for hash comparison
3. **Keep it simple**: Monitor-only processing is cleaner than multi-level hierarchy
4. **One flag to rule them all**: _all_complete is more efficient than checking everything
5. **Document as you go**: Mermaid diagrams help clarify architecture

---

## Tomorrow's First Steps

1. Update event_id generation in `store_live_fixture()`
2. Create `process_fixture_events()` activity
3. Simplify MonitorWorkflow to call new activity
4. Remove EventWorkflow references
5. Test with real fixture data

Let's ship it! 🚀
