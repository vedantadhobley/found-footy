# Staging Interval Polling

## Overview

Staging fixtures are polled every 15 minutes instead of every 30 seconds, reducing API requests by ~97%.

## How It Works

### The `_last_monitor` Field

Every fixture in staging has a `_last_monitor` datetime field:
- **Set at ingestion** when fixture enters staging
- **Updated on poll** when we fetch fresh API data
- **Used to calculate interval** for determining if fixture needs polling

### Interval Calculation

```python
def get_interval(dt: datetime) -> int:
    """Convert datetime to 15-minute interval (0-95 per day)"""
    return (dt.hour * 4) + (dt.minute // 15)

# Examples:
# 08:00-08:14 → interval 32
# 08:15-08:29 → interval 33
# 08:30-08:44 → interval 34
```

### Polling Logic

On each monitor cycle (~30s):

1. Calculate `current_interval` from now
2. For each staging fixture, calculate `fixture_interval` from `_last_monitor`
3. If `fixture_interval != current_interval` → add to poll list
4. If poll list empty → skip API call entirely
5. If poll list not empty → fetch those fixtures, update `_last_monitor = now`
6. Pre-activate any fixtures with kickoff <= now + 30min

### Timing Example

```
08:00:05 - Monitor runs → current_interval = 32
           27 fixtures have interval from ingestion (e.g., 28)
           Action: Fetch all 27, set _last_monitor (now interval 32)
           
08:00:35 - Monitor runs → current_interval = 32
           All fixtures have interval 32
           Action: Skip API call ✓
           
08:15:05 - Monitor runs → current_interval = 33
           All fixtures have interval 32 (not 33!)
           Action: Fetch all 27, set _last_monitor (now interval 33)
           
08:15:35 - Monitor runs → current_interval = 33
           All fixtures have interval 33
           Action: Skip ✓
```

## Pre-Activation

When a staging fixture has `kickoff <= now + 30min`:
- Move to `fixtures_active`
- Set `_activated_at = now`
- Do NOT set `_last_activity` (stays null)
- Delete from staging

Pre-activated fixtures appear at the bottom of the frontend (sorted by kickoff) until they actually kick off.

## Emergency Activation (Failsafe)

If a staging fixture's status is already active (`1H`, `HT`, `2H`, `ET`, `BT`, `P`), we **immediately** move it to active regardless of kickoff time. This catches:

- Games that started earlier than scheduled
- API data anomalies
- Any situation where we'd otherwise miss monitoring a live game

```
🚨 [MONITOR] EMERGENCY ACTIVATION | fixture=123456 | 
   match=Team A vs Team B | status=1H | 
   Game started while still in staging!
```

## `_last_activity` vs `_last_monitor`

| Field | Purpose | Set When |
|-------|---------|----------|
| `_last_monitor` | Track when fixture was last polled from API | Ingestion, every poll |
| `_last_activity` | Frontend sorting (recent activity at top) | Match kicks off (NS→1H), goal scored |

## Request Savings

| Scenario | Before | After |
|----------|--------|-------|
| 27 staging fixtures/day | ~5,760 requests | ~192 requests |
| Reduction | - | **97%** |

## Files Changed

- `src/data/models.py` - Added `FixtureFields.LAST_MONITOR`
- `src/data/mongo_store.py` - Set `_last_monitor` at ingestion and in `sync_fixture_data()`
- `src/activities/monitor.py` - `pre_activate_upcoming_fixtures()` does interval comparison
- `src/workflows/monitor_workflow.py` - Simplified to just call the activity every run

## Verified Behavior

From production logs:

```
📋 [MONITOR] Staging poll | interval=39 (09:45) | polling 27/27 fixtures
📊 [MONITOR] Staging complete | polled=27 | updated=27 | activated=0
✅ Monitor complete (0.7s): staging=polled 27 (27 updated/0 activated), ...

📋 [MONITOR] Staging skip | interval=39 (09:46) | all 27 fixtures in current interval
✅ Monitor complete (0.1s): staging=skipped (0 updated/0 activated), ...

📋 [MONITOR] Staging skip | interval=39 (09:47) | all 27 fixtures in current interval
✅ Monitor complete (0.1s): staging=skipped (0 updated/0 activated), ...
```

- First run in interval 39 (09:45): Polls all 27 fixtures, takes ~12s
- Subsequent runs in interval 39: Skip API call entirely, completes in 0.1s
