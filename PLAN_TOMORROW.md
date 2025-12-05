# Found Footy - Tomorrow's Work Plan
**Date**: December 5, 2024  
**Branch**: `refactor/temporal-migration`

---

## 🎯 Today's Accomplishments (December 4, 2024)

### 1. **Workflow Naming Improvements** ✅
Changed all workflow IDs to be human-readable:
- **IngestWorkflow**: `ingest-04_12_2024`
- **MonitorWorkflow**: `monitor-04_12_2024-15:23`
- **EventWorkflow**: `event-Liverpool-vs-Arsenal-1378993`
- **TwitterWorkflow**: `twitter-Liverpool-Salah-45+3min-1378993_40_Goal_1`
- **DownloadWorkflow**: `download-Liverpool-Salah-3vids-1378993_40_Goal_1`

**Key changes**:
- Full team names with underscores (not abbreviations)
- Special character handling: `A.C. Milan` → `A_C_Milan`
- Minutes include extra time: `45+3min`
- Files modified: `event_workflow.py`, `twitter_workflow.py`, `download_workflow.py`, `worker.py`

### 2. **Fixture Data Syncing** ✅
Added `sync_fixture_data()` to keep fixture metadata fresh:
- Updates score, status, time from `fixtures_live` → `fixtures_active`
- Preserves enhanced events array
- Called after debounce operations
- Also called when no debounce needed (keeps data fresh)
- Files: `mongo_store.py`, `event.py`, `monitor_workflow.py`, `monitor.py`

### 3. **Documentation** ✅
Created `TEMPORAL_WORKFLOWS.md` with:
- Complete workflow explanations with sequence diagrams
- 4-collection MongoDB architecture
- Debounce logic breakdown (4 cases)
- Event data structure evolution
- Troubleshooting guide
- **8 Mermaid diagrams** showing data flow

### 4. **Iterate-and-Drop Pattern** ✅
Optimized debounce logic in `event.py`:
- Build dict of live events for O(1) lookup
- Iterate active events, pop from dict as processed
- Leftover dict items = new events
- Simpler than set comparisons

---

## 🚨 Critical Issue Discovered Tonight

### **Event ID Must Include Player ID**

**Current event_id format**:
```python
event_id = f"{fixture_id}_{team_id}_{event_type}_{sequence}"
# Example: "1378993_40_Goal_1"
```

**Problem**: If Salah scores → VAR disallows → Firmino scores, both get `_Goal_1`. Same ID, different player!

**Solution**: Include player_id in event_id:
```python
event_id = f"{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}"
# Example: "1378993_40_306_Goal_1"  (306 = Salah's player ID)
```

Now if player changes → event_id changes → treated as new event (correct behavior).

---

## 🔄 Architecture Pivot: Simplify to Monitor-Only Processing

### Current Architecture (Complex)
```
MonitorWorkflow
  └─> EventWorkflow (per fixture)
        └─> debounce_fixture_events (iterate all events, hash comparison)
              └─> TwitterWorkflow (per stable event)
```

### Proposed Architecture (Simpler)
```
MonitorWorkflow
  ├─> Compare event_ids (set operations)
  ├─> Update active events (new/removed/increment stable_count)
  ├─> Trigger TwitterWorkflow directly (when stable_count >= 3)
  └─> Sync fixture metadata
```

**Key insight**: With player_id in event_id, we don't need hash comparison! Event ID change = different event.

### Benefits
- ✅ Eliminate EventWorkflow entirely
- ✅ No hash computation needed
- ✅ Simpler logic (set comparisons only)
- ✅ All processing in one place
- ✅ Still trigger TwitterWorkflow per event (parallelism preserved)

### Implementation Details

#### 1. Update Event ID Generation
**File**: `src/data/mongo_store.py` → `store_live_fixture()`

```python
# Current
event_id = f"{fixture_id}_{team_id}_{event_type}_{sequence}"

# New
player_id = event.get("player", {}).get("id", 0)
event_id = f"{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}"
```

#### 2. Simplify MonitorWorkflow
**File**: `src/workflows/monitor_workflow.py`

```python
async def run(self):
    # Activate fixtures
    await activate_fixtures()
    
    # Fetch active fixtures
    fixtures = await fetch_active_fixtures()
    
    twitter_workflows_triggered = []
    
    for fixture_data in fixtures:
        fixture_id = fixture_data["fixture"]["id"]
        
        # Store in live (generates event_ids with player_id)
        await store_and_compare(fixture_id, fixture_data)
        
        # Process events directly (no EventWorkflow child)
        comparison = await process_fixture_events(fixture_id)
        
        # Trigger TwitterWorkflow for newly stable events
        for event_info in comparison["twitter_triggered"]:
            workflow_id = build_twitter_workflow_id(event_info)
            await trigger_twitter_workflow(
                fixture_id, 
                event_info["event_id"],
                event_info["player_name"],
                event_info["team_name"]
            )
            twitter_workflows_triggered.append(workflow_id)
        
        # Check completion
        if fixture_status in ["FT", "AET", "PEN"]:
            await complete_fixture_if_ready(fixture_id)
    
    return {
        "fixtures_processed": len(fixtures),
        "twitter_workflows_triggered": len(twitter_workflows_triggered)
    }
```

#### 3. New Activity: process_fixture_events
**File**: `src/activities/monitor.py`

```python
@activity.defn
async def process_fixture_events(fixture_id: int) -> Dict[str, Any]:
    """
    Process fixture events using simple set comparison.
    No hash needed - event_id includes player_id.
    
    Algorithm:
    1. Get event_ids from live and active
    2. Compare sets:
       - new_events = live_ids - active_ids → add to active with stable_count=1
       - removed_events = active_ids - live_ids → mark as removed
       - matching_events = live_ids & active_ids → increment stable_count
    3. For matching events with stable_count >= 3:
       - Mark _debounce_complete
       - Return event info for TwitterWorkflow trigger
    4. Sync fixture metadata
    """
    store = FootyMongoStore()
    
    live = store.get_live_fixture(fixture_id)
    active = store.get_fixture_from_active(fixture_id)
    
    live_ids = {e["_event_id"] for e in live["events"]}
    active_ids = {e["_event_id"] for e in active["events"]}
    
    # NEW events
    new_events = live_ids - active_ids
    for event_id in new_events:
        live_event = next(e for e in live["events"] if e["_event_id"] == event_id)
        store.add_event_to_active(fixture_id, enhance_event(live_event))
    
    # REMOVED events (VAR)
    removed_events = active_ids - live_ids
    for event_id in removed_events:
        store.mark_event_removed(fixture_id, event_id)
    
    # MATCHING events - increment stable_count
    matching_events = live_ids & active_ids
    twitter_triggered = []
    
    for event_id in matching_events:
        active_event = next(e for e in active["events"] if e["_event_id"] == event_id)
        
        if active_event.get("_debounce_complete"):
            continue  # Already done
        
        new_count = active_event.get("_stable_count", 0) + 1
        store.increment_stable_count(fixture_id, event_id, new_count)
        
        if new_count >= 3:
            store.mark_event_debounce_complete(fixture_id, event_id)
            twitter_triggered.append({
                "event_id": event_id,
                "player_name": active_event["player"]["name"],
                "team_name": active_event["team"]["name"],
                "minute": active_event["time"]["elapsed"],
                "extra": active_event["time"].get("extra")
            })
    
    # Sync fixture metadata
    store.sync_fixture_data(fixture_id)
    
    return {
        "new_events": len(new_events),
        "removed_events": len(removed_events),
        "updated_events": len(matching_events),
        "twitter_triggered": twitter_triggered
    }
```

---

## 📋 Tomorrow's Tasks (December 5, 2024)

### Priority 1: Architecture Refactor
- [ ] Update event_id generation to include player_id
- [ ] Remove EventWorkflow entirely
- [ ] Implement `process_fixture_events` activity in MonitorWorkflow
- [ ] Update worker.py to remove EventWorkflow registration
- [ ] Test with real fixtures

### Priority 2: _all_complete Flag Pattern
- [ ] Add `_all_complete` field to fixtures in `fixtures_active`
- [ ] Create `recalculate_all_complete(fixture_id)` function
- [ ] Call after ANY event status update:
  - `_debounce_complete`
  - `_twitter_complete`
  - `_download_complete`
  - Future: `_sentiment_complete`
- [ ] Update `complete_fixture_if_ready` to check single flag

### Priority 3: Twitter Implementation
Current status: Stub exists, needs full implementation

**Files to update**: `src/activities/twitter.py`, `src/workflows/twitter_workflow.py`

**Implementation**:
```python
@activity.defn
async def search_event_videos(fixture_id: int, event_id: str, player_name: str):
    # 1. Get event from fixtures_active
    event = store.get_event(fixture_id, event_id)
    search_query = event["_twitter_search"]  # e.g., "Salah Liverpool"
    
    # 2. POST to twitter-session service
    response = requests.post(
        "http://twitter-session:8888/search",
        json={"query": search_query},
        timeout=120
    )
    
    # 3. Parse response
    videos = response.json().get("videos", [])
    
    # 4. Save to event and mark complete
    store.update_event(fixture_id, event_id, {
        "discovered_videos": videos
    })
    store.mark_event_twitter_complete(fixture_id, event_id)
    store.recalculate_all_complete(fixture_id)  # Check if fixture done
    
    return {
        "video_count": len(videos),
        "videos": videos,
        "player_name": player_name
    }
```

### Priority 4: Download & Deduplication
Current status: Stub exists, needs full implementation

**Strategy**: Deduplicate BEFORE upload (not after)

**Files to update**: `src/activities/download.py`

**Port from Prefect implementation**:
- Download logic with yt-dlp
- MD5 hash calculation
- Deduplication (keep largest per hash)
- S3 upload with metadata

**Implementation**:
```python
@activity.defn
async def download_and_upload_videos(fixture_id: int, event_id: str):
    # 1. Get discovered videos
    event = store.get_event(fixture_id, event_id)
    video_urls = event["discovered_videos"]
    
    # 2. Download all videos to /tmp
    downloaded = []
    for url in video_urls:
        filepath = ytdlp.download(url, output_dir="/tmp")
        file_size = os.path.getsize(filepath)
        md5_hash = calculate_md5(filepath)
        downloaded.append({
            "path": filepath,
            "hash": md5_hash,
            "size": file_size,
            "url": url
        })
    
    # 3. Deduplicate - keep largest per hash
    hash_groups = {}
    for video in downloaded:
        hash_key = video["hash"]
        if hash_key not in hash_groups:
            hash_groups[hash_key] = video
        elif video["size"] > hash_groups[hash_key]["size"]:
            # Delete smaller duplicate
            os.remove(hash_groups[hash_key]["path"])
            hash_groups[hash_key] = video
        else:
            # Delete this smaller duplicate
            os.remove(video["path"])
    
    unique_videos = list(hash_groups.values())
    
    # 4. Upload to S3
    s3_urls = []
    for video in unique_videos:
        s3_key = f"{fixture_id}/{event_id}/{video['hash']}.mp4"
        s3_url = s3_client.upload(
            video["path"],
            bucket="found-footy-videos",
            key=s3_key,
            metadata={
                "player": event["player"]["name"],
                "team": event["team"]["name"],
                "event_id": event_id,
                "fixture_id": str(fixture_id),
            }
        )
        s3_urls.append(s3_url)
    
    # 5. Save S3 URLs and mark complete
    store.update_event(fixture_id, event_id, {"s3_urls": s3_urls})
    store.mark_event_download_complete(fixture_id, event_id)
    store.recalculate_all_complete(fixture_id)  # Check if fixture done
    
    # 6. Cleanup
    for video in unique_videos:
        os.remove(video["path"])
    
    return {
        "uploaded_count": len(unique_videos),
        "s3_urls": s3_urls
    }
```

**Directory structure in S3**:
```
found-footy-videos/
  {fixture_id}/
    {event_id}/
      {md5_hash_1}.mp4
      {md5_hash_2}.mp4
```

### Priority 5: Testing
- [ ] Test event_id generation with player_id
- [ ] Test MonitorWorkflow with simplified logic
- [ ] Test Twitter search (mock if no real credentials)
- [ ] Test download & deduplication with sample videos
- [ ] Test _all_complete flag updates

---

## 🔮 Future Features (Not Tomorrow, But Soon)

### 1. Sentiment Analysis
Add `_sentiment_complete` and `_sentiment_score` to events:
```python
# After TwitterWorkflow completes
SentimentWorkflow(fixture_id, event_id)
  └─> Fetch tweet comments
  └─> Run through Hugging Face sentiment model
  └─> Assign star rating (1-5)
  └─> Mark _sentiment_complete
  └─> Recalculate _all_complete
```

### 2. Visual Verification
Before upload, verify videos are actually soccer:
```python
# In DownloadWorkflow, before upload
for video in unique_videos:
    frames = extract_frames(video["path"])
    is_soccer = vision_model.verify(frames)  # Visual intelligence
    if not is_soccer:
        os.remove(video["path"])  # Don't upload weird stuff
```

### 3. Red Card Support
Already supported in event_config.py, just disabled:
```python
TRACKABLE_EVENTS = {
    "Goal": True,
    "Card": True,  # Enable this
}
```

Event data structure already handles it (same pattern as goals).

### 4. Bulk Monitor Optimization
Fetch staging + active fixtures in ONE API call:
- Update fixtures in both collections
- Auto-activate when status becomes LIVE
- Wipe events array when moving staging → active

---

## 📊 Current System State

### Collections
- **fixtures_staging**: Holds TBD/NS fixtures
- **fixtures_live**: Temporary comparison buffer (overwritten each poll)
- **fixtures_active**: Enhanced with debounce data (never fully overwritten)
- **fixtures_completed**: Archive of finished fixtures

### Workflows
- **IngestWorkflow**: Daily at 00:05 UTC (PAUSED)
- **MonitorWorkflow**: Every minute (ENABLED)
- ~~**EventWorkflow**: Per fixture with changes~~ → **TO BE REMOVED**
- **TwitterWorkflow**: Per stable event (WORKING)
- **DownloadWorkflow**: Per event with videos (STUB)

### Event Enhancement Fields
```python
{
    "_event_id": "1378993_40_306_Goal_1",  # WILL INCLUDE PLAYER_ID
    "_stable_count": 2,
    "_debounce_complete": False,
    "_twitter_complete": False,
    "_download_complete": False,
    "_sentiment_complete": False,  # FUTURE
    "_all_complete": False,  # NEW FLAG (fixture-level)
    "_first_seen": "...",  # Can remove (debug only)
    "_snapshots": [...],   # Can remove (debug only)
    "_twitter_search": "Salah Liverpool",
    "_score_before": "0-0",
    "_score_after": "1-0",
    "_removed": False,
    "discovered_videos": [...],  # Added by TwitterWorkflow
    "s3_urls": [...],  # Added by DownloadWorkflow
    "_sentiment_score": 4.2,  # FUTURE
}
```

---

## 🎯 Success Criteria for Tomorrow

By end of day, we should have:
1. ✅ Event IDs include player_id
2. ✅ EventWorkflow removed, logic moved to MonitorWorkflow
3. ✅ _all_complete flag pattern implemented
4. ✅ TwitterWorkflow fully implemented (or mocked)
5. ✅ DownloadWorkflow with deduplication implemented
6. ✅ End-to-end test: Ingest → Monitor → Twitter → Download → Complete

---

## 🐛 Known Issues to Fix

1. **fixtures_live not being dropped**: By design - it's overwritten per fixture
2. **Data ordering**: Preserved from API (chronological)
3. **Fixtures not advancing to completed**: Need _all_complete flag pattern
4. **Hash comparison overhead**: Eliminated with player_id in event_id

---

## 📝 Notes

- Keep Prefect implementation as reference for download/dedupe logic
- Twitter session service runs on port 8888
- S3 bucket: `found-footy-videos`
- MongoDB Express: `localhost:4101`
- Temporal UI: `localhost:4100`
- Worker restart: `docker compose -f docker-compose.dev.yml restart worker`

---

## 💭 Architectural Decisions Made

1. **Player ID in Event ID**: Required for accurate change detection
2. **Monitor-Only Processing**: Simpler than EventWorkflow child pattern
3. **No Hash Comparison**: Event ID change = different event (sufficient)
4. **_all_complete Flag**: More efficient than checking all events every time
5. **Dedupe Before Upload**: Saves S3 bandwidth and storage
6. **Full Team Names**: Better readability in Temporal UI
7. **Fixture Data Sync**: Keep metadata fresh even without debounce

---

## 🚀 Let's Ship This Tomorrow!

The architecture is simpler, the logic is clearer, and we're eliminating unnecessary complexity. We've got a solid plan - let's execute! 💪
