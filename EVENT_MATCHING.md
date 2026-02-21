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
2. `T+25:00` — API corrects: Garcia own goal (player 619), not Griezmann. `team.id` stays 530 (Atletico benefited). Old event disappears, new event: `1520391_530_619_Goal_1`. `_score_after` unchanged (`"1-0"`).
3. `T+25:00` — Old event enters drop pipeline. Videos scheduled for deletion.
4. `T+27:30` — Old event deleted (3 drop cycles). S3 videos gone.
5. `T+27:30` — New event starts fresh debounce.
6. `T+29:00` — New event stable (3 polls). TwitterWorkflow starts.
7. `T+39:00` — TwitterWorkflow finishes. But searching 30 minutes late yields poor results.

**The videos we deleted at T+27:30 literally showed the same goal moment.**

---

## Solution: Same-Cycle Event Matching

When an event is removed and a new event appears **in the same monitor cycle**, check if they represent the same goal moment. If they match, carry the old event's state (videos, workflows, tracking fields) over to the new event instead of starting fresh.

### Matching Criteria: `_score_after` as Primary Signal

Two events match if ALL of the following are true:

| Criterion | Why |
|-----------|-----|
| **Same monitor cycle** | Removed and added in the same `process_fixture_events` call |
| **Same `_score_after`** | Identical score fingerprint = same physical goal moment |
| **Same event `type`** | Both are "Goal" type events (guard against type changes) |

### Why `_score_after` Works

`_score_after` is a string like `"1-0"` representing what the scoreboard shows after this goal. It's a **unique fingerprint per goal** within a fixture — no two goals produce the same score transition.

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

`_score_after` eliminates this entirely — the first goal produces `"1-0"`, the second produces `"2-0"`. They can never collide.

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

### What Gets Carried Over

When a match is found, the new event **inherits** the old event's accumulated state:

| Field | Carry Over? | Notes |
|-------|-------------|-------|
| `_monitor_workflows` | ✅ Yes | Already debounced — don't re-debounce |
| `_monitor_complete` | ⚠️ Conditional | Only if `_download_complete=true` (see edge case #5) |
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
3. NEW: Try to match removed events with new events using _score_after
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
    fixture_data: dict,              # for score_after computation on new events
) -> list[tuple[str, str]]:          # [(removed_id, new_id), ...]
    """
    Find removed/new event pairs that represent the same goal moment.
    
    Primary signal: exact _score_after match.
    Fallback: same minute + same type (for events without _score_after).
    
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
        removed_score = removed_event.get("_score_after")
        removed_minute = removed_event.get("time", {}).get("elapsed")
        removed_type = removed_event.get("type")
        
        if removed_type != "Goal":
            continue  # Only match goal events
        
        for new_id in new_ids:
            if new_id in used_new:
                continue
            new_event = next(
                (e for e in live_events if e.get("_event_id") == new_id), None
            )
            if not new_event or new_event.get("type") != "Goal":
                continue
            
            new_score = compute_score_after(new_event, fixture_data)
            new_minute = new_event.get("time", {}).get("elapsed")
            
            # Primary: exact _score_after match
            if removed_score and new_score and removed_score == new_score:
                matches.append((removed_id, new_id))
                used_new.add(new_id)
                used_removed.add(removed_id)
                break
            
            # Fallback: same minute, no _score_after available
            if (not removed_score and removed_minute is not None
                    and removed_minute == new_minute):
                matches.append((removed_id, new_id))
                used_new.add(new_id)
                used_removed.add(removed_id)
                break
    
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
        EventFields.DOWNLOAD_WORKFLOWS: removed_event.get(EventFields.DOWNLOAD_WORKFLOWS, []),
        EventFields.DOWNLOAD_COMPLETE: removed_event.get(EventFields.DOWNLOAD_COMPLETE, False),
        EventFields.FIRST_SEEN: removed_event.get(EventFields.FIRST_SEEN),
        EventFields.DISCOVERED_VIDEOS: removed_event.get(EventFields.DISCOVERED_VIDEOS, []),
        EventFields.S3_VIDEOS: removed_event.get(EventFields.S3_VIDEOS, []),
        EventFields.DOWNLOAD_STATS: removed_event.get(EventFields.DOWNLOAD_STATS, {}),
        "_matched_from": removed_id,  # Audit trail
    }
    
    # _monitor_complete: only carry if downloads are done (see edge case #5)
    if removed_event.get(EventFields.DOWNLOAD_COMPLETE, False):
        carried_state[EventFields.MONITOR_COMPLETE] = True
    else:
        # Downloads not done — don't carry _monitor_complete.
        # The old TwitterWorkflow has stale player_name baked in.
        # Let the new event trigger a fresh TwitterWorkflow with correct name.
        carried_state[EventFields.MONITOR_COMPLETE] = False
    
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

### 1. Multiple goals at the same minute — impossible to mis-pair

Two Team A goals at minute 45 both get removed and re-added. With `_score_after`, these **cannot be mis-paired**: the first goal produces `"1-0"` and the second `"2-0"`. They match uniquely even though they share a minute.

This is the primary advantage of `_score_after` over minute-based matching.

### 2. VAR disallowed goal + coincidental new goal

Team A scores at minute 30 (`_score_after: "1-0"`). VAR removes it (event disappears, no replacement). In the same cycle, Team B scores at minute 30 (`_score_after: "0-1"`). Different `_score_after` values — **no match occurs**. Correct behavior.

**Worst case:** Same team scores, same minute, same `_score_after` (VAR removes and team immediately re-scores). Requires both happening in the same ~30s monitor cycle. Effectively impossible in practice.

### 3. Event flickers across multiple cycles

Event disappears in cycle N, reappears in N+1, disappears in N+2 with a new player. Matching only runs within a single cycle, so this never triggers matching. The event follows the normal drop path. Correct — cross-cycle changes aren't simple re-attributions.

### 4. Own goal re-attribution — `_score_after` is stable

When Normal Goal → Own Goal, the API changes `player_id` and `detail` but keeps `team.id` the same — the benefiting team was always correct (it's obvious which net the ball went in). Since `calculate_score_context()` uses `team.id` to determine which side of the score to increment, `_score_after` is identical for both attributions. Exact `_score_after` matching handles this with no special logic.

### 5. In-flight TwitterWorkflow

If the old event had `_monitor_complete=true` and a TwitterWorkflow is running, that workflow has stale `player_name` and `event_id` baked into its `TwitterWorkflowInput` (set once at start, never re-read from MongoDB). It continues searching with the old player name and starting DownloadWorkflows for the old event ID.

**V1 approach — conditional `_monitor_complete` carry:**
- If `_download_complete=true`: carry `_monitor_complete=true`. All done, no in-flight issues.
- If `_download_complete=false`: set `_monitor_complete=false`. The stale TwitterWorkflow fails gracefully when it can't find the old event ID in MongoDB. The new event triggers a fresh TwitterWorkflow with the correct player name. Some download slots are "wasted" on the orphaned workflow, but the new one searches with the right terms.

**Future (v2) — signal-based update:** Push updated `player_name` and `event_id` to the running TwitterWorkflow via Temporal signals. Avoids the gap but adds complexity.

### 6. No match found

If a removed event has no matching new event (different `_score_after`, different type, etc.), it follows the existing normal path: drop workflow tracking, deletion after 3 cycles.

### 7. Minute drift (45→46, timing correction)

The API sometimes corrects event timing. `_score_after` is unchanged — same physical goal, same score transition. Primary `_score_after` matching handles this without needing minute comparison.

---

## What Doesn't Change

- **Event ID format** — stays `{fixture_id}_{team_id}_{player_id}_{type}_{sequence}`
- **S3 key format** — opaque keys, no event ID embedded. Safe to carry over.
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

This creates an audit trail: `_matched_from: "1520391_529_619_Goal_1"` on the new event shows it inherited state from the original Garcia attribution.

---

## Files to Modify

| File | Change |
|------|--------|
| `src/activities/monitor.py` | Add matching logic in `process_fixture_events()` between new/removed computation and processing |
| `src/data/models.py` | Add `MATCHED_FROM = "_matched_from"` to `EventFields` |
| `src/utils/event_enhancement.py` | Expose `compute_score_after()` for matching (may already exist within `calculate_score_context`) |
| `src/data/mongo_store.py` | Possibly add a `delete_event_without_s3()` helper (or reuse existing `$pull`) |

---

## Implementation Order

| # | Task | Notes |
|---|------|-------|
| 1 | Add `MATCHED_FROM` to `EventFields` | Trivial |
| 2 | Write `find_event_matches()` helper | Pure function, easy to unit test |
| 3 | Write `create_matched_event()` helper | Builds new event with carried state + conditional `_monitor_complete` |
| 4 | Integrate into `process_fixture_events()` | Insert between new/removed computation |
| 5 | Unit tests for matching logic | Test all edge cases above |
| 6 | Integration test with mock fixture data | Simulate re-attribution scenario |
