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

### Real Example: Garcia Own Goal (Atletico Madrid vs Barcelona, 2026-02-12)

Fixture `1520391` — Atletico Madrid 4-0 Barcelona. Garcia own goal at 6'.

1. `T+0:00` — Goal at 6'. API attributes to Griezmann (Atletico, team 530). Event ID: `1520391_530_194_Goal_1`. Score: 1-0. Videos downloaded, uploaded to S3.
2. `T+25:00` — API corrects: Garcia own goal (player 619), not Griezmann. `team.id` stays 530 (Atletico benefited). Old event disappears, new event: `1520391_530_619_Goal_1`. `_score_after` unchanged.
3. `T+25:00` — Old event enters drop pipeline. Videos scheduled for deletion.
4. `T+27:30` — Old event deleted (3 drop cycles). S3 videos gone.
5. `T+27:30` — New event starts fresh debounce.
6. `T+29:00` — New event stable (3 polls). TwitterWorkflow starts.
7. `T+39:00` — TwitterWorkflow finishes. But searching 30 minutes late yields poor results.

**The videos we deleted at T+27:30 literally showed the same goal moment.**

---

## Solution: Drop-Window Video Transfer

When the API re-attributes a goal, we don't need pointers or event remapping. We just need to **transfer the already-downloaded videos** from the old event to the new one before S3 cleanup runs.

The old event enters the drop pipeline when it disappears (3 unique monitor cycles to deletion). The new event enters normal debounce when it appears (3 cycles to ready). These windows can overlap, have a gap, or the new event can arrive first. **Two matching hooks** cover all timing scenarios — one fires when the new event appears, the other fires when the old event is about to be deleted.

### Matching Criteria

Two events represent the same goal if:

| Criterion | Why |
|-----------|-----|
| **Same `_score_after`** | Identical score fingerprint = same physical goal moment |
| **Same event `type`** | Both are "Goal" (safety guard) |
| **Minute within ±2** | Additional verification with leeway for timing corrections and extra time |

### Why `_score_after` Works

`_score_after` is a dict like `{"home": 1, "away": 0}` representing the score after this goal. It's a **unique fingerprint per goal** within a fixture — no two goals produce the same score transition.

When a goal is re-attributed (scorer correction, goal→own goal, assist change), the **score doesn't change** — the same ball went in the same net. `_score_after` is computed by `calculate_score_context()` in [event_enhancement.py](src/utils/event_enhancement.py) and is stable across all re-attribution types:

| Re-Attribution Type | What Changes | `_score_after` | Matches? |
|---------------------|-------------|----------------|----------|
| Scorer correction (Garcia → Gonzalez) | `player_id`, event ID | Same | ✅ |
| Goal → Own Goal | `player_id`, `detail`, event ID | Same | ✅ |
| Own Goal → Goal | `player_id`, `detail`, event ID | Same | ✅ |
| Assist added/changed | `assist` field only (event ID unchanged) | Same | N/A (no ID change) |
| Minute drift (45→46) | `time.elapsed` | Same | ✅ |
| VAR disallowed goal | Event removed entirely, no replacement | N/A | No match (correct) |

### Why `_score_after` Is Better Than Minute + Scoring Team

The original approach matched on `time.elapsed` + `_scoring_team`. This had a weakness: **two goals at the same minute by the same team could be mis-paired** (e.g., a 45' goal and a 45+2' goal both showing `elapsed=45`).

`_score_after` eliminates this entirely — the first goal produces `{"home": 1, "away": 0}`, the second produces `{"home": 2, "away": 0}`. They can never collide.

### API Verification: Own Goal Attribution

Verified against real API data from Atletico Madrid 4-0 Barcelona (fixture `1520391`, 2026-02-12):

```json
{
  "time": {"elapsed": 6, "extra": null},
  "team": {"id": 530, "name": "Atletico Madrid"},
  "player": {"id": 619, "name": "E. Garcia"},
  "type": "Goal",
  "detail": "Own Goal"
}
```

**Key finding:** `team.id = 530` (Atletico Madrid) — the **benefiting team**, not Garcia's team (Barcelona, 529). This means `calculate_score_context()` is correct: it increments the score for `event.team.id`, which is always the team that gained the goal regardless of normal/own-goal attribution.

**Key implication for matching:** `team.id` does **not** change during re-attributions. The API always identifies the correct benefiting team — it's obvious which net the ball went in. What changes is the *player attribution* (who scored it) and possibly the *detail* (Normal Goal → Own Goal). Since `team.id` stays the same, `calculate_score_context()` produces the same `_score_after` for both the original and corrected events. **Exact `_score_after` matching works for all re-attribution types, including own goals.**

---

### What Gets Carried Over (Video State)

When a match is found, we transfer the old event's video/download state to the new event:

| Field | Transfer? | Notes |
|-------|-----------|-------|
| `_s3_videos` | ✅ Yes | **Critical** — uploaded videos for this goal moment |
| `_discovered_videos` | ✅ Yes | URLs already found |
| `_download_stats` | ✅ Yes | Pipeline statistics |
| `_download_workflows` | ✅ Yes | Downloads already registered |
| `_download_complete` | ✅ Yes | May already be done |
| `_first_seen` | ✅ Yes | Original detection time |
| `_monitor_workflows` | ⚠️ Conditional | Only if `_download_complete=true` |
| `_monitor_complete` | ⚠️ Conditional | Only if `_download_complete=true` (see edge case #5) |
| `_twitter_aliases` | ❌ No | Recompute — player name may have changed |
| `_twitter_search` | ❌ No | Recompute — player name may have changed |
| `_score_after` | ❌ No | Recompute from live data |
| `_scoring_team` | ❌ No | Recompute from live data |
| `_drop_workflows` | ❌ No | Reset (event is present now) |
| `_monitor_count` | ❌ No | Deprecated field, reset |

**If `_download_complete=true`:** Everything is done. Carry all state including `_monitor_complete=true`. New event is immediately fully complete — no wasted work.

**If `_download_complete=false`:** Only carry video/discovery state. Set `_monitor_complete=false`, clear `_monitor_workflows`. The new event debounces normally and triggers a fresh TwitterWorkflow with the correct player name.

### What the New Event Gets

- **New `_event_id`** — generated from the corrected player attribution
- **New raw API fields** — `player`, `team`, `type`, `detail`, `time` from the live event
- **Transferred video state** — `_s3_videos`, `_discovered_videos`, etc.
- **Recomputed context** — `_twitter_search`, `_twitter_aliases`, `_score_after`, `_scoring_team`
- **New field: `_matched_from`** — the old event ID, for audit trail

### What Happens to the Old Event

The old event is **deleted without S3 cleanup** — videos now belong to the new event.

---

## Timing Scenarios

The API swap is not atomic. The old event may disappear cycles before the new one appears, or the new one may appear before the old disappears.

### Scenario 1: New arrives while old is dropping (gap ≥ 0 cycles)

| Cycle | Old Event | New Event |
|-------|-----------|-----------|  
| 0 | Disappears → drop 1 | Not visible |
| 1 | drop 2 | **Appears → Hook A fires**: match found, videos transferred. Old deleted (no S3 cleanup). |
| 2 | Gone | debounce 2 (videos already attached) |
| 3 | — | debounce 3 → TwitterWorkflow with correct player name. **Videos safe.** |

This covers both a multi-cycle gap and a same-cycle swap (gap = 0).

### Scenario 2: New arrives before old disappears (overlap)

| Cycle | Old Event | New Event |
|-------|-----------|-----------|  
| 0 | Still present | **Appears** as new event (debounce 1). No match — old isn't dropping. |
| 1 | Still present | debounce 2 |
| 2 | **Disappears** → drop 1 | debounce 3 → TwitterWorkflow triggers |
| 3 | drop 2 | downloading... |
| 4 | **drop 3 → deletion threshold → Hook B fires**: videos transferred to new event. Old deleted (no S3 cleanup). | receives videos |

---

## Two Hooks

Both hooks call the same core function: `find_score_match()`.

### Hook A — New Goal event appears

In the NEW events section of `process_fixture_events()`, when adding a new Goal event:

1. Compute its `_score_after` from live fixture data
2. Scan `active_map` for any Goal event with:
   - Same `_score_after` (dict equality)
   - Non-empty `_drop_workflows` (it's in the drop pipeline)
   - Minute within ±2
3. If match found → transfer video state, delete old event (no S3 cleanup), add new event with carried state
4. If no match → normal new event flow

### Hook B — Old event reaches deletion threshold

In the REMOVED events section, when `should_delete=true` and `monitor_complete=true` (S3 cleanup about to happen):

1. Before calling `store.mark_event_removed()`, scan `active_map` for any Goal event with:
   - Same `_score_after` (dict equality)
   - Different `_event_id` (not itself)
   - Minute within ±2
2. If match found → transfer videos to the match, delete old event (no S3 cleanup)
3. If no match → normal deletion with S3 cleanup (existing behavior)

---

## Algorithm

### Core: `find_score_match()`

```python
MINUTE_TOLERANCE = 2

def find_score_match(
    score_after: dict,              # {"home": int, "away": int}
    minute: int,
    exclude_event_id: str,          # Don't match against self
    active_map: dict[str, dict],
    require_dropping: bool = False, # Hook A sets True
) -> str | None:
    """
    Find an active Goal event with the same _score_after.
    Returns the matched event_id, or None.
    """
    if not score_after:
        return None
    
    for event_id, event in active_map.items():
        if event_id == exclude_event_id:
            continue
        if event.get("type") != "Goal":
            continue
        if require_dropping and not event.get(EventFields.DROP_WORKFLOWS):
            continue
        
        event_score = event.get(EventFields.SCORE_AFTER)
        event_minute = event.get("time", {}).get("elapsed")
        
        if not event_score or event_score != score_after:
            continue
        
        # Verify minute within tolerance
        if (minute is not None and event_minute is not None
                and abs(minute - event_minute) > MINUTE_TOLERANCE):
            continue
        
        return event_id
    
    return None
```

### Hook A integration (NEW events section)

```python
# Inside the new_ids loop, before adding the event:
live_event = next(e for e in live_events if e.get(EventFields.EVENT_ID) == event_id)

if live_event.get("type") == "Goal":
    score_context = calculate_score_context(live_fixture, live_event)
    new_score_after = score_context.get(EventFields.SCORE_AFTER)
    new_minute = live_event.get("time", {}).get("elapsed")
    
    matched_id = find_score_match(
        score_after=new_score_after,
        minute=new_minute,
        exclude_event_id=event_id,
        active_map=active_map,
        require_dropping=True,  # Only match events in drop pipeline
    )
    
    if matched_id:
        transfer_video_state(store, fixture_id, active_map[matched_id], matched_id,
                             live_event, event_id, live_fixture)
        continue  # Skip normal new event flow
```

### Hook B integration (REMOVED events section)

```python
# Inside the removed_ids loop, when should_delete and monitor_complete:
if should_delete and monitor_complete:
    # Before S3 cleanup, check if a replacement event already exists
    removed_score = active_event.get(EventFields.SCORE_AFTER)
    removed_minute = active_event.get("time", {}).get("elapsed")
    
    matched_id = find_score_match(
        score_after=removed_score,
        minute=removed_minute,
        exclude_event_id=event_id,
        active_map=active_map,
        require_dropping=False,  # Match ANY active event
    )
    
    if matched_id:
        transfer_video_state_to_existing(store, fixture_id, active_event, event_id, matched_id)
    else:
        store.mark_event_removed(fixture_id, event_id)  # Normal S3 cleanup
```

### `transfer_video_state()` (Hook A — new event being added)

```python
def transfer_video_state(store, fixture_id, old_event, old_id,
                          new_live_event, new_id, fixture_data):
    """Transfer videos from dropping event to new event being added."""
    carried = {
        EventFields.S3_VIDEOS: old_event.get(EventFields.S3_VIDEOS, []),
        EventFields.DISCOVERED_VIDEOS: old_event.get(EventFields.DISCOVERED_VIDEOS, []),
        EventFields.DOWNLOAD_STATS: old_event.get(EventFields.DOWNLOAD_STATS, {}),
        EventFields.DOWNLOAD_WORKFLOWS: old_event.get(EventFields.DOWNLOAD_WORKFLOWS, []),
        EventFields.DOWNLOAD_COMPLETE: old_event.get(EventFields.DOWNLOAD_COMPLETE, False),
        EventFields.FIRST_SEEN: old_event.get(EventFields.FIRST_SEEN),
        EventFields.MATCHED_FROM: old_id,
    }
    
    # If downloads are complete, carry monitor state too (event is fully done)
    if old_event.get(EventFields.DOWNLOAD_COMPLETE, False):
        carried[EventFields.MONITOR_COMPLETE] = True
        carried[EventFields.MONITOR_WORKFLOWS] = old_event.get(EventFields.MONITOR_WORKFLOWS, [])
    
    # Delete old event WITHOUT S3 cleanup
    store.fixtures_active.update_one(
        {"_id": fixture_id},
        {"$pull": {"events": {EventFields.EVENT_ID: old_id}}}
    )
    
    # Create new event with carried state + fresh API data
    enhanced = create_matched_event(new_live_event, new_id, carried, fixture_data)
    store.add_event_to_active(fixture_id, enhanced, carried[EventFields.FIRST_SEEN])
    
    log.warning(activity.logger, MODULE, "event_matched",
                "Re-attributed event matched — videos transferred",
                old_event_id=old_id, new_event_id=new_id,
                hook="new_event",
                videos_transferred=len(carried.get(EventFields.S3_VIDEOS, [])),
                download_complete=carried.get(EventFields.DOWNLOAD_COMPLETE, False))
```

### `transfer_video_state_to_existing()` (Hook B — existing event receives videos)

```python
def transfer_video_state_to_existing(store, fixture_id, old_event, old_id, target_id):
    """Transfer videos from event about to be deleted to existing active event."""
    videos = old_event.get(EventFields.S3_VIDEOS, [])
    discovered = old_event.get(EventFields.DISCOVERED_VIDEOS, [])
    
    # Merge videos onto the target event
    store.fixtures_active.update_one(
        {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": target_id},
        {
            "$addToSet": {
                f"events.$.{EventFields.S3_VIDEOS}": {"$each": videos},
                f"events.$.{EventFields.DISCOVERED_VIDEOS}": {"$each": discovered},
            },
            "$set": {
                f"events.$.{EventFields.MATCHED_FROM}": old_id,
            },
        }
    )
    
    # Delete old event WITHOUT S3 cleanup
    store.fixtures_active.update_one(
        {"_id": fixture_id},
        {"$pull": {"events": {EventFields.EVENT_ID: old_id}}}
    )
    
    log.warning(activity.logger, MODULE, "event_matched",
                "Re-attributed event matched — videos transferred to existing event",
                old_event_id=old_id, target_event_id=target_id,
                hook="deletion_threshold",
                videos_transferred=len(videos))
```

---

## Edge Cases

### 1. Multiple goals at the same minute — impossible to mis-pair

Two Team A goals at minute 45 both get re-attributed. With `_score_after`, these **cannot be mis-paired**: the first has `{"home": 1, "away": 0}` and the second `{"home": 2, "away": 0}`. They match uniquely despite sharing a minute.

### 2. VAR disallowed goal + coincidental new goal

Team A scores at minute 30 (`_score_after: {"home": 1, "away": 0}`). VAR removes it. In a later cycle, Team B scores at minute 30 (`_score_after: {"home": 0, "away": 1}`). Different `_score_after` — **no match**. Correct behavior.

### 3. Event flickers (disappears and reappears with same ID)

Event disappears in cycle N, reappears with the same `_event_id` in N+1. Handled by existing `clear_drop_workflows` in the matching_ids section. No event matching needed.

### 4. Own goal re-attribution — `_score_after` is stable

When Normal Goal → Own Goal, the API changes `player_id` and `detail` but keeps `team.id` the same — the benefiting team was always correct. `_score_after` is identical for both attributions. Exact matching handles this with no special logic.

### 5. In-flight TwitterWorkflow (conditional state carry)

**If `_download_complete=true`:** Carry all state including `_monitor_complete=true`. Event is fully done — no wasted work.

**If `_download_complete=false`:** A stale TwitterWorkflow may be running with the old player name. Set `_monitor_complete=false` on the new event. It debounces normally → triggers fresh TwitterWorkflow with correct player name. Stale workflow orphans gracefully when it can't find the old event ID in MongoDB.

### 6. No match found

Events follow existing behavior:
- Removed events: normal drop pipeline → deletion after 3 cycles
- New events: normal debounce → fresh Twitter search

### 7. Minute drift (45→46, timing correction)

`_score_after` doesn't depend on `time.elapsed` — same physical goal, same score transition. The ±2 minute tolerance accommodates any drift.

### 8. Hook A and Hook B can't both fire for the same pair

Hook A fires when the new event appears and finds a dropping match — old event is deleted immediately. By the time Hook B would fire (old reaches 3 drops), the old event is already gone.

### 9. Extra time goals (45+3, 90+5)

`time.elapsed` shows the base minute (45, 90), with extra time in `time.extra`. Two goals at 45+1 and 45+3 both have `elapsed=45` — but their `_score_after` values differ, so they can't be confused. The ±2 tolerance on elapsed applies cleanly.

---

## What Doesn't Change

- **Event ID format** — stays `{fixture_id}_{team_id}_{player_id}_{type}_{sequence}`
- **S3 key format** — opaque keys, no event ID embedded. Safe to transfer.
- **Normal debounce flow** — new events still go through 3-poll debounce
- **Normal drop flow** — events without matches still go through 3-drop deletion
- **S3 video structure** — videos transfer as-is, no re-upload needed
- **UploadWorkflow** — unchanged, uses event ID from MongoDB
- **DownloadWorkflow** — unchanged
- **TwitterWorkflow** — unchanged (fresh trigger with correct player name when needed)
- **Frontend** — no changes needed. New event has its own ID and correct data.

---

## Observability

### New Log Actions

| Action | Level | When |
|--------|-------|------|
| `event_matched` | WARNING | Videos transferred between events (logged by both hooks) |

Logged fields: `old_event_id`, `new_event_id` or `target_event_id`, `hook` (`new_event` or `deletion_threshold`), `videos_transferred`, `download_complete`.

### New Event Field

| Field | Type | Set By | When |
|-------|------|--------|------|
| `_matched_from` | string | Hook A or Hook B | When event receives videos from a re-attributed event |

Audit trail: `_matched_from: "1520391_530_194_Goal_1"` shows this event inherited videos from the original Griezmann attribution.

---

## Files to Modify

| File | Change |
|------|--------|
| `src/activities/monitor.py` | Add `find_score_match()`, Hook A in new events loop, Hook B in removed events loop, `transfer_video_state()`, `transfer_video_state_to_existing()` |
| `src/data/models.py` | Add `MATCHED_FROM = "_matched_from"` to `EventFields` |

---

## Implementation Order

| # | Task | Notes |
|---|------|-------|
| 1 | Add `MATCHED_FROM` to `EventFields` | Trivial |
| 2 | Write `find_score_match()` | Core matching function, used by both hooks |
| 3 | Write `transfer_video_state()` | For Hook A (new event being added) |
| 4 | Write `transfer_video_state_to_existing()` | For Hook B (existing event receives videos) |
| 5 | Integrate Hook A into new events loop | Check for dropping match before normal add |
| 6 | Integrate Hook B into removed events loop | Check for active match before S3 cleanup |
| 7 | Unit tests for `find_score_match()` | Test all edge cases |
| 8 | Integration test | Simulate re-attribution with both timing scenarios |
