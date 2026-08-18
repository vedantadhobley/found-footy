# Python ingest and monitor behavior

Frozen legacy behavior from the [Python functional-spec index](./README.md).

## 3. Ingest Behavior

### Trigger

**Daily at 00:05 UTC** via Temporal scheduler. Also triggerable
manually via Temporal UI with optional `target_date` or `fixture_ids`
parameters.

**File:** `archive/src/workflows/ingest_workflow.py:54-353`

### Fetch Strategy (Smart Lookahead)

**Standard behavior** (when tomorrow has fixtures):
1. Fetch **today** (UTC)
2. Fetch **tomorrow** (needed for timezone edge cases)
3. Fetch **day after** (timezone coverage)

**Lookahead behavior** (when tomorrow is empty):
1. Fetch **today** + **tomorrow** (always needed)
2. Search forward up to **30 days** to find the next date with fixtures
3. Fetch that date + the day after

This ensures the frontend always has **at least one future date
with fixtures to display**, even during international breaks or
off-season gaps.

**File:** `archive/src/workflows/ingest_workflow.py:106-250`

### Team Filter (Tracked Leagues)

Only fixtures involving tracked teams are fetched. Team ID set
computed dynamically per-league (top-flight teams) + static national
team IDs.

**File:** `archive/src/utils/team_data.py`

### Categorization Rules

Fixtures are routed based on `fixture.status.short`:

| Status | Collection | Description |
| --- | --- | --- |
| TBD, NS | **fixtures_staging** | Not started |
| LIVE, 1H, HT, 2H, ET, P, BT, SUSP, INT, PST | **fixtures_active** | In progress or delayed |
| FT, AET, PEN, CANC, ABD, WO, AWD | **fixtures_completed** | Finished |

**Note:** PST (postponed) is treated as **ACTIVE** to handle short
delays (15-30 min), not moved back to staging. If a match is
postponed for hours/days, it stays in active until either it resumes
or it's cancelled.

**File:** `archive/src/activities/ingest.py:112-226`
(categorize_and_store_fixtures)

### Duplicate Handling

Fixtures are **only added if they don't already exist** in staging/
active/completed. If a fixture exists, ingest **skips it entirely**
— the monitor workflow handles live updates to existing fixtures.

Check: `store.get_staging_fixture_ids() + store.get_active_fixture_ids()
+ store.get_completed_fixtures()`

**File:** `archive/src/activities/ingest.py:140-162`

### RAG Alias Pre-Caching

For each unique team in the ingested fixtures, ingest **immediately
caches team aliases via the RAG pipeline** (Wikidata + LLM). This is
**per-team, blocking per-team** (not per-fixture), so failures don't
cascade:

- Tries **cache lookup first** (MongoDB team_aliases)
- If **cache miss**: runs full **RAG pipeline** (~30-90 seconds) to
  fetch Wikidata aliases and LLM-select best Twitter words
- If **RAG fails**: logs warning and continues (aliases will be
  regenerated on-demand during Twitter search)

**File:** `archive/src/workflows/ingest_workflow.py:263-307`

### Retention Cleanup

At the **end of ingest**, fixtures older than **14 days** are deleted
from MongoDB + S3:

- Cutoff = today (when ingest runs, at 00:05) - 14 days
- Deletes all fixtures with `fixture.date < cutoff`
- Removes S3 blobs for those fixtures

**File:** `archive/src/activities/ingest.py:228-260` (cleanup_old_fixtures)

### Frontend Notification

Ingest calls `notify_frontend_refresh()` at the end to trigger SSE
broadcast, letting the frontend know new fixtures are available.

---

## 4. Monitor Behavior

### Trigger

**Every 30 seconds**, continuously, via Temporal schedule. Wakes up,
polls active fixtures, processes events, and completes.

**File:** `archive/src/workflows/monitor_workflow.py:45-318`

### Staging Fixture Polling (15-Minute Intervals)

Before polling active fixtures, monitor checks staging fixtures for
pre-activation. To reduce API calls by ~97%, it uses a **15-minute
interval system**:

- Interval calculation: `(hour * 4) + (minute // 15)` → 0-95 per day
  - 08:00-08:14 → interval 32
  - 08:15-08:29 → interval 33
  - 08:30-08:44 → interval 34

- Only fetches staging fixtures **not in the current 15-minute
  interval** (tracked via `_last_monitor` timestamp)
- All other fixtures stay in MongoDB without API call

Pre-activation happens when `fixture.date <= now + 30 minutes`:
- Moves fixture from **fixtures_staging** → **fixtures_active**
- Sets `_activated_at = now`
- **Does NOT set `_last_activity`** (that's only set when match
  actually starts)

**Failsafe:** If a fixture status is already active (1H, 2H, HT,
etc.) when still in staging, immediately activate with data. This
catches games that started early or API anomalies.

**File:** `archive/src/activities/monitor.py:149-293`
(pre_activate_upcoming_fixtures)

### Active Fixture Polling (Full Batch)

Monitor fetches **all active fixture IDs** from `fixtures_active`
collection, then batch-fetches from API-Football in one call.

Returns raw API data with full events arrays.

**File:** `archive/src/activities/monitor.py:295-326`
(fetch_active_fixtures)

### Event Detection (Pure Set Comparison)

For each active fixture, monitor:

1. **Stores live data** in `fixtures_live` (temporary buffer)
2. **Compares** events from live vs. active (both as sets of `_event_id`s)
3. **Classifies into 3 buckets:**
   - **NEW** = live - active → add to active (if player known)
   - **REMOVED** = active - live → decrement, possibly delete (VAR)
   - **MATCHING** = live ∩ active → debounce check, trigger condition

Event ID format: `{fixture_id}_{team_id}_{player_id}_{type}_{sequence}`

Example: `1234567_40_90_Goal_1` (fixture 1234567, Liverpool [40],
Salah [90], first goal)

This ID is **stable across API mutations** to time/name/extra field
changes — only the player, team, type, and sequence matter for
identity.

**File:** `archive/src/activities/monitor.py:486-761`
(process_fixture_events)

### Debouncing (Workflow-ID-Based Tracking)

**New events** (live - active):
- Start with **`_monitor_workflows = []`** (empty array)
- Will not trigger Twitter until they debounce

**Matching events** (live ∩ active):
- Add current **MonitorWorkflow ID** to `_monitor_workflows` via
  `$addToSet` (idempotent)
- Check: **`len(_monitor_workflows) >= 3 AND _monitor_complete == false`**
- If true → spawn TwitterWorkflow

This means **3 unique monitor runs must see an event** before
triggering Twitter. API glitches that miss an event for 1-2 polls
won't trigger false positives.

**Special case:** If player is not known (player.id = 0 or name =
"Unknown"), the event stays at `_monitor_workflows = []` until a
player name is populated. When that happens, a **new event_id is
created** (sequence increments), and the old "Unknown" event is
eventually VAR'd.

**File:** `archive/src/activities/monitor.py:643-750`

### VAR Reversal Handling (Drop Workflows)

**Removed events** (active - live):

1. **Unknown scorer check:** If `player_id == 0` or name is
   "Unknown", **delete immediately** (no wait). These are clearly
   placeholder events that got replaced by the actual scorer.

2. **Known scorer:** Track with `_drop_workflows` array:
   - Add current MonitorWorkflow ID via `$addToSet`
   - Check: **`len(_drop_workflows) >= 3`**
   - If yes → **delete the event** (and S3 videos if
     `_monitor_complete == true`)
   - If no → log "event missing" and continue monitoring

3. **Event reappears** (was in drop_workflows, now in live):
   **FULL RESET** — clear `_drop_workflows` entirely. Drop count
   starts from 0 if event disappears again.

This handles API flickering gracefully — one reappearance = full
reset, so transient glitches don't cause data loss.

**File:** `archive/src/activities/monitor.py:565-640`

### Fixture Completion Check

After processing all events, monitor checks if fixture can be moved
to completed. See [§8](./completion-and-coordination.md#8-fixture-completion-behavior) for full
detail.

### Fixture Metadata Sync

Monitor calls `sync_fixture_data()` at the end of event processing
to update fixture-level fields:
- **`_last_activity`**: Set to now when status changes from NS/TBD
  to any active status (1H, 2H, etc.). This is used for frontend
  sorting (active matches with activity at top).
- **`_last_monitor`**: Updated to current time (used by staging
  interval check)

### Parallel Processing

Monitor processes **all active fixtures in parallel** via
`asyncio.gather(return_exceptions=True)`. If one fixture fails
(activity error, Mongo error), other fixtures are unaffected.

**File:** `archive/src/workflows/monitor_workflow.py:247-249`

### Frontend Notification

If any events were triggered for Twitter, monitor calls
`notify_frontend_refresh()` to broadcast SSE event.

---
