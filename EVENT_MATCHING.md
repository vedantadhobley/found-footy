# Event Matching — Re-Attribution Recovery

**Branch:** `feature/event-matching`  
**Parent:** `main` at `8386d96`

## Problem

When the API re-attributes a goal — player name changes, goal becomes own goal, scorer correction — the event disappears from the API and a new event appears in its place. Because our event ID encodes `player_id` and `team_id`:

```
{fixture_id}_{team_id}_{player_id}_{type}_{sequence}
```

...the new event gets a completely different ID. The current system treats this as two independent events:

1. **Old event (removed)**: Enters the drop workflow pipeline. After 3 monitor cycles (~90 seconds), it's deleted along with all its S3 videos.
2. **New event (detected)**: Starts from scratch — debounce from 0, wait 3 polls (~90s), resolve aliases, search Twitter 10 times (~10 min).

**By the time the new event finishes searching, 30+ minutes have passed since the actual goal.** Twitter's freshest video results are long gone. The videos we already had from the original event — which showed the same goal — are deleted.

### Real Example: Garcia Own Goal (2026-02-17)

1. `T+0:00` — Goal detected. Event ID: `123456_100_Garcia_Goal_1`. Videos downloaded, uploaded to S3.
2. `T+25:00` — API corrects: it was an own goal. Event `123456_100_Garcia_Goal_1` disappears. New event `123456_200_OpponentPlayer_Goal_1` appears (own goals get attributed to the scoring team, different `team_id`).
3. `T+25:00` — Old event enters drop pipeline. Videos scheduled for deletion.
4. `T+27:30` — Old event deleted (3 drop cycles). S3 videos gone.
5. `T+27:30` — New event starts fresh debounce.
6. `T+29:00` — New event stable (3 polls). TwitterWorkflow starts.
7. `T+39:00` — TwitterWorkflow finishes. But searching "Garcia own goal" 30 minutes late yields poor results.

**The videos we deleted at T+27:30 literally showed the same goal moment.** We just didn't know the attribution would change.

---

## Solution: Same-Cycle Event Matching

When an event is removed and a new event appears **in the same monitor cycle**, check if they represent the same goal moment. If they match, carry the old event's state (videos, workflows, tracking fields) over to the new event instead of starting fresh.

### Matching Criteria

Two events match if ALL of the following are true:

| Criterion | Why |
|-----------|-----|
| **Same monitor cycle** | Removed and added in the same `process_fixture_events` call |
| **Same minute** | `time.elapsed` matches (both events refer to the same match moment) |
| **Same scoring team** | The team that scored the goal is the same (see below) |

### Why "Same Scoring Team" Works

This is the key insight. When a goal is re-attributed, the **scoring team** doesn't change — only the credited player does:

| Scenario | Old Event | New Event | Scoring Team |
|----------|-----------|-----------|--------------|
| Player name correction | Garcia (Team A) Goal | Gonzalez (Team A) Goal | Team A ✓ |
| Goal → Own Goal | Garcia (Team A) Goal | Opponent (Team B) Goal | Team A ✓ (see below) |
| Assist change | Same player, different assist | Same player, different assist | Same team ✓ |

**Own goal nuance:** When a goal becomes an own goal, the API changes the `team_id` on the event. Garcia (Team A) scored → becomes OwnGoal by Opponent (Team B). But **Team A still scored** — the ball went into Team B's net. We need to derive "which team scored" from the match context (the team that gained a goal), not from `event.team.id`.

#### Deriving Scoring Team

We already compute this — `_scoring_team` is set by `calculate_score_context()` in [src/utils/event_enhancement.py](src/utils/event_enhancement.py). This field represents "which team gained a point from this event" and is stable across re-attributions.

However, for the matching logic we need to compare scoring teams between the **removed** active event and the **new** live event. The removed event already has `_scoring_team` stored. The new event needs it computed from the live fixture data.

### What Gets Carried Over

When a match is found, the new event **inherits** the old event's accumulated state:

| Field | Carry Over? | Notes |
|-------|-------------|-------|
| `_monitor_workflows` | ✅ Yes | Already debounced — don't re-debounce |
| `_monitor_complete` | ✅ Yes | Twitter already started |
| `_download_workflows` | ✅ Yes | Downloads already registered |
| `_download_complete` | ✅ Yes | May already be done |
| `_first_seen` | ✅ Yes | Original detection time |
| `_discovered_videos` | ✅ Yes | URLs already found |
| `_s3_videos` | ✅ Yes | **Critical** — preserve uploaded videos |
| `_download_stats` | ✅ Yes | Pipeline statistics |
| `_twitter_aliases` | ❌ No | Recompute — player name changed |
| `_twitter_search` | ❌ No | Recompute — player name changed |
| `_score_after` | ❌ No | Recompute from live data |
| `_scoring_team` | ❌ No | Recompute from live data |
| `_drop_workflows` | ❌ No | Reset (event is present now) |
| `_monitor_count` | ❌ No | Deprecated field, reset |

### What the New Event Gets

The new event is stored with:
- **New `_event_id`** — the new event ID (new player, new team for OG)
- **New raw API fields** — `player`, `team`, `type`, `detail`, `time` from the live event
- **Carried-over tracking state** — all the fields in the "✅ Yes" column above
- **Recomputed context** — `_twitter_search`, `_twitter_aliases`, `_score_after`, `_scoring_team`
- **New field: `_matched_from`** — the old event ID, for audit trail

### What Happens to the Old Event

The old event is **deleted without S3 cleanup** — because we're transferring its videos to the new event, not discarding them.

---

## Algorithm

Inside `process_fixture_events()`, between computing `new_ids` / `removed_ids` and processing them:

```
1. Compute new_ids = live_ids - active_ids
2. Compute removed_ids = active_ids - live_ids
3. NEW: Try to match removed events with new events
4. Process unmatched new events (normal flow)
5. Process unmatched removed events (normal drop flow)
6. Process matched pairs (carry over state)
7. Process matching_ids (normal flow, unchanged)
```

### Matching Algorithm (Step 3)

```python
def find_event_matches(
    new_ids: set[str],
    removed_ids: set[str],
    live_events: list[dict],
    active_events: dict[str, dict],  # active_map
    fixture_data: dict,              # for scoring team derivation
) -> list[tuple[str, str]]:          # [(removed_id, new_id), ...]
    """
    Find removed/new event pairs that represent the same goal moment.
    
    Returns list of (removed_event_id, new_event_id) tuples.
    Each event ID can appear in at most ONE match (1:1 matching).
    """
    matches = []
    used_new = set()
    used_removed = set()
    
    for removed_id in removed_ids:
        if removed_id in used_removed:
            continue
        removed_event = active_events[removed_id]
        removed_minute = removed_event.get("time", {}).get("elapsed")
        removed_scoring_team = removed_event.get("_scoring_team")
        
        if removed_minute is None:
            continue
        
        for new_id in new_ids:
            if new_id in used_new:
                continue
            new_event = next(
                (e for e in live_events if e.get("_event_id") == new_id), None
            )
            if not new_event:
                continue
            
            new_minute = new_event.get("time", {}).get("elapsed")
            new_scoring_team = derive_scoring_team(new_event, fixture_data)
            
            if (new_minute == removed_minute 
                    and new_scoring_team == removed_scoring_team):
                matches.append((removed_id, new_id))
                used_new.add(new_id)
                used_removed.add(removed_id)
                break  # Move to next removed event
    
    return matches
```

### Processing Matched Pairs (Step 6)

```python
for removed_id, new_id in matched_pairs:
    removed_event = active_map[removed_id]
    new_live_event = next(e for e in live_events if e.get("_event_id") == new_id)
    
    # Build new event with carried-over state
    carried_state = {
        EventFields.MONITOR_WORKFLOWS: removed_event.get(EventFields.MONITOR_WORKFLOWS, []),
        EventFields.MONITOR_COMPLETE: removed_event.get(EventFields.MONITOR_COMPLETE, False),
        EventFields.DOWNLOAD_WORKFLOWS: removed_event.get(EventFields.DOWNLOAD_WORKFLOWS, []),
        EventFields.DOWNLOAD_COMPLETE: removed_event.get(EventFields.DOWNLOAD_COMPLETE, False),
        EventFields.FIRST_SEEN: removed_event.get(EventFields.FIRST_SEEN),
        EventFields.DISCOVERED_VIDEOS: removed_event.get(EventFields.DISCOVERED_VIDEOS, []),
        EventFields.S3_VIDEOS: removed_event.get(EventFields.S3_VIDEOS, []),
        EventFields.DOWNLOAD_STATS: removed_event.get(EventFields.DOWNLOAD_STATS, {}),
        "_matched_from": removed_id,  # Audit trail
    }
    
    # Delete old event (NO S3 cleanup — videos are being transferred)
    store.fixtures_active.update_one(
        {"_id": fixture_id},
        {"$pull": {"events": {EventFields.EVENT_ID: removed_id}}}
    )
    
    # Add new event with carried state + fresh API data
    enhanced = create_matched_event(
        live_event=new_live_event,
        event_id=new_id,
        carried_state=carried_state,
        fixture_data=fixture_data,
    )
    store.add_event_to_active(fixture_id, enhanced, carried_state[EventFields.FIRST_SEEN])
    
    log.warning(activity.logger, MODULE, "event_matched",
                "Re-attributed event matched — state carried over",
                old_event_id=removed_id, new_event_id=new_id,
                minute=new_live_event.get("time", {}).get("elapsed"),
                videos_carried=len(carried_state.get(EventFields.S3_VIDEOS, [])),
                monitor_complete=carried_state[EventFields.MONITOR_COMPLETE],
                download_complete=carried_state.get(EventFields.DOWNLOAD_COMPLETE, False))
```

---

## Edge Cases

### 1. Multiple goals at the same minute by the same team

Two Team A goals at minute 45 both get removed and re-added. Since we match 1:1 and break after the first match per removed event, this could mis-pair. 

**Mitigation:** This is extremely rare — two goals at the exact same minute by the same team that both get re-attributed in the same cycle. If it happens, the worst case is swapping which set of videos goes to which event. Both events keep their videos (no data loss), just potentially swapped. Acceptable trade-off.

### 2. Genuine VAR removal + coincidental new goal at same minute

Team A scores at minute 30, VAR removes it. Meanwhile, Team A scores a legitimate new goal also at minute 30. These would incorrectly match.

**Mitigation:** Extremely unlikely — VAR reviews take seconds, not minutes, and two goals at the same minute by the same team is already rare. If it happens, the new goal inherits stale videos (which show the same general match moment). The pipeline will still search for fresh videos via any remaining Twitter attempts. Again, no data loss — just some stale videos in the mix temporarily.

### 3. Event flickers across multiple cycles

Event disappears in cycle N, reappears in cycle N+1, disappears in N+2 with a new player. Since matching only happens within a single cycle (same call to `process_fixture_events`), this won't trigger matching. The event reappearance in N+1 clears `_drop_workflows` (existing behavior), and the N+2 disappearance starts a fresh drop count.

**Result:** No matching occurs. The event follows the normal remove → drop → delete path. This is correct — if the event reappeared in between, the API wasn't doing a simple re-attribution.

### 4. Own goal: team_id changes

Event ID includes `team_id`. A goal by Player X (Team A) becoming an own goal means the API now attributes it to Team B. So both `team_id` AND `player_id` change in the event ID.

**Why matching still works:** We compare `_scoring_team` (from `calculate_score_context`), which is "which team gained a goal." Team A gained the goal in both cases. The `team_id` in the event ID is different, but the scoring team is the same.

### 5. In-flight TwitterWorkflow or DownloadWorkflow

If the old event had `_monitor_complete=true` and a TwitterWorkflow is currently running with the old event ID, it will continue running (fire-and-forget, ABANDON policy). It will try to search and start DownloadWorkflows for the old event ID, but those will fail to find the event in MongoDB (it's been deleted and replaced with the new ID).

**Mitigation:** The carried-over `_download_workflows` count may be less than what the old TwitterWorkflow registered. When the new event is eventually picked up by a fresh TwitterWorkflow (if `_monitor_complete=false`) or if `_download_complete` is already true, the state is consistent. The orphaned old TwitterWorkflow will error gracefully on `check_event_exists` and terminate.

**Better option (future):** If `_monitor_complete=true` but `_download_complete=false`, we could avoid re-triggering Twitter and instead just let the remaining downloads play out. But the old event ID is gone from MongoDB, so in-flight workflows for it will fail. We may need to handle this more carefully — perhaps by NOT carrying over `_monitor_complete` if downloads aren't done yet, so the new event goes through a fresh Twitter cycle with the correct player name. Worth considering during implementation.

### 6. No match found (genuinely different events)

If a removed event has no matching new event (different minute, different scoring team, etc.), it follows the existing normal path: drop workflow tracking, deletion after 3 cycles.

---

## What Doesn't Change

- **Event ID format** — stays `{fixture_id}_{team_id}_{player_id}_{type}_{sequence}`
- **Normal debounce flow** — events without matching still go through 3-poll debounce
- **Normal drop flow** — events without matching still go through 3-drop deletion
- **S3 video structure** — videos carry over as-is, no re-upload needed
- **UploadWorkflow** — unchanged, uses event ID from MongoDB
- **DownloadWorkflow** — unchanged
- **TwitterWorkflow** — unchanged (but may get a fresh trigger with new player name)

---

## Observability

### New Log Actions

| Action | Level | When |
|--------|-------|------|
| `event_matched` | WARNING | Removed event matched to new event — state carried over |
| `event_match_attempted` | INFO | Matching attempted (logs candidate counts) |
| `event_match_none` | DEBUG | No matches found in this cycle |

### New Event Field

| Field | Type | Set By | When |
|-------|------|--------|------|
| `_matched_from` | string | MonitorWorkflow | When event is matched from a removed event |

This creates an audit trail: `_matched_from: "123456_100_Garcia_Goal_1"` on the new event shows it inherited state from the Garcia attribution.

---

## Files to Modify

| File | Change |
|------|--------|
| `src/activities/monitor.py` | Add matching logic in `process_fixture_events()` between new/removed computation and processing |
| `src/data/models.py` | Add `MATCHED_FROM = "_matched_from"` to `EventFields` |
| `src/utils/event_enhancement.py` | May need to expose `derive_scoring_team()` for matching |
| `src/data/mongo_store.py` | Possibly add a `delete_event_without_s3()` helper (or reuse existing `$pull`) |

---

## Implementation Order

| # | Task | Notes |
|---|------|-------|
| 1 | Add `MATCHED_FROM` to `EventFields` | Trivial |
| 2 | Write `find_event_matches()` helper | Pure function, easy to unit test |
| 3 | Write `create_matched_event()` helper | Builds new event with carried state |
| 4 | Integrate into `process_fixture_events()` | Insert between new/removed computation |
| 5 | Unit tests for matching logic | Test all edge cases above |
| 6 | Integration test with mock fixture data | Simulate re-attribution scenario |
