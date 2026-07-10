# Python Functional Specification — found-footy

## Preamble

This document describes **WHAT the Python found-footy system DOES**
— its behaviors, contracts, invariants, and edge cases — **NOT HOW
it's implemented, how to redesign it, or what bugs exist.** It's the
authoritative reference for the system's actual functional behavior,
useful for:

- **Go rewrite implementation**: Design against this spec, not
  against code archaeology
- **User/PM understanding**: What does the system actually guarantee,
  and when can it fail?
- **Testing**: Every claim here should be verifiable by a functional test
- **Design conversations**: Answer "does Python do X?" without reading
  code

This spec **complements** [`rebuild-plan.md`](../rebuild-plan.md),
which describes the *target* architecture. This spec captures the
*actual* behavior of the Python production system, workflow-by-
workflow, so the Go rewrite can be a faithful mechanical translation
of intent (with any deliberate divergences flagged in
[`../decisions.md`](../decisions.md)).

**Cross-references:** When this spec says "§4.2 references §5.1,"
it's linking within this document. When it cites a file like
`archive/src/workflows/twitter_workflow.py:249-284`, that's a live
code location. When it says "UNCLEAR from code, would need to test,"
it means the code is ambiguous or the behavior is undocumented and
the Go rewrite team should determine intent from the user before
picking a behavior.

**Also**: some observations in this spec are marked `BUG?` — those
are things that read like defects to a fresh reader. They are NOT
proposals to fix in Python; they're behavior notes so the Go rewrite
can decide whether to preserve or correct.

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [Data Schema](#2-data-schema)
3. [Ingest Behavior](#3-ingest-behavior)
4. [Monitor Behavior](#4-monitor-behavior)
5. [Twitter Search / Discovery Behavior](#5-twitter-search--discovery-behavior)
6. [Download / Video Validation Behavior](#6-download--video-validation-behavior)
7. [Upload / Asset Persistence Behavior](#7-upload--asset-persistence-behavior)
8. [Fixture Completion Behavior](#8-fixture-completion-behavior)
9. [Cross-Workflow Coordination](#9-cross-workflow-coordination)
10. [Failure Modes & Recovery](#10-failure-modes--recovery)
11. [Edge Cases & Corner Behaviors](#11-edge-cases--corner-behaviors)
12. [Configuration Reference](#12-configuration-reference)
13. [External Dependencies & Contracts](#13-external-dependencies--contracts)
14. [Retention & Cleanup Policies](#14-retention--cleanup-policies)
15. [Observability & Telemetry](#15-observability--telemetry)
16. [Gaps in This Spec](#16-gaps-in-this-spec)

---

## 1. System Overview

### The Core Loop

found-footy is a **real-time goal-clip video discovery and delivery
pipeline** for tracked professional football (soccer) leagues and
national teams. The system runs 24/7 and operates on a simple core
loop:

```
INGEST (daily 00:05 UTC)
  ↓
MONITOR (every 30s, active fixtures only)
  ↓
EVENT DETECT (set-based diff: live vs. active)
  ↓
DEBOUNCE (3 consecutive polls see event)
  ↓
TWITTER SEARCH (fire-and-forget, up to 15 attempts at 60s spacing)
  ↓
DOWNLOAD + VALIDATE (parallel per-video, 3 retries each)
  ↓
UPLOAD + DEDUP (serialized per-event, S3 + MongoDB)
  ↓
FRONTEND NOTIFY (SSE broadcast)
```

Each stage is **horizontally independent** — failures in download
don't block upload, fixture tracking, or monitor cycles. The system
targets **near-real-time coverage**: detect a goal in the API →
search Twitter within seconds → first video uploaded within 2-5
minutes.

### Core Concepts in the Real World

| Concept | What It Represents | Lifetime |
| --- | --- | --- |
| **Fixture** | A single football match (e.g., Liverpool vs. Man City, 2025-12-26, 15:00 UTC) | Created at ingest, expires 14 days after finish |
| **Event** | A single goal scored in a fixture (e.g., "Salah scores at 42'") | Created when API detects it, deleted if VAR'd, expires with fixture |
| **Video** | A clip of the goal from a social media source (e.g., Twitter video) | Discovered via Twitter search, validated by AI, uploaded to S3, expires with fixture |
| **Tweet** | A social media post containing a goal video | Not persisted; used to extract video URL, then discarded |
| **Alias** | A variant name for a team (e.g., "LFC", "Reds" for Liverpool) | Cached in MongoDB at ingest time, refreshed per-team via RAG |

### What Gets Monitored

The system tracks fixtures in **6 top-tier European leagues + FIFA
national teams:**

- Premier League (39)
- La Liga (140)
- Bundesliga (78)
- Serie A (135)
- Ligue 1 (61)
- UEFA Champions League (2)
- UEFA Europa League (3)
- UEFA Conference League (848)
- FIFA World Cup (1), UEFA Euro (4), and other continental tournaments
  when in season

**What gets tracked within each fixture:** Only **Goal events** (not
cards, substitutions, VAR reviews). Within goals, only **"Normal
Goal", "Penalty", and "Own Goal"** detail types (not "Missed Penalty"
and not red-card-related events).

---

## 2. Data Schema

### MongoDB Collections and Lifecycle

The system maintains **4 fixture collections** representing stages
of the match lifecycle:

```
┌──────────────────┐
│ fixtures_staging │  (not started: NS, TBD)
└────────┬─────────┘
         │ [pre-activate when kickoff <= now + 30min]
         ↓
┌──────────────────┐
│ fixtures_active  │  (in progress: 1H, HT, 2H, ET, P, BT, LIVE, PST, etc.)
└────────┬─────────┘
         │ [all events complete + status is Terminal + completion counter]
         ↓
┌─────────────────────┐
│ fixtures_completed  │  (finished: FT, AET, PEN, CANC, ABD, WO, AWD)
└─────────────────────┘
```

Plus:

- **fixtures_live**: Temporary buffer for raw API poll data
  (overwritten each monitor cycle)
- **team_aliases**: RAG-derived team name variants for Twitter search
- **video_shares**: Not currently used, legacy structure

### Fixture Collection Schema

Each fixture document (`_id` = API-Football fixture ID) contains:

#### Raw API Fields (from api-football)

```json
{
  "_id": 1234567,
  "fixture": {
    "id": 1234567,
    "referee": "John Doe",
    "timezone": "UTC",
    "date": "2025-12-26T15:00:00Z",
    "timestamp": 1234567890,
    "status": {
      "long": "Match Finished",
      "short": "FT",
      "elapsed": 90,
      "extra": null
    }
  },
  "league": {
    "id": 39, "name": "Premier League", "country": "England",
    "logo": "...", "flag": "...", "season": 2025,
    "round": "Regular Season - 19"
  },
  "teams": {
    "home": { "id": 40, "name": "Liverpool", "logo": "...", "winner": true },
    "away": { "id": 33, "name": "Manchester City", "logo": "...", "winner": false }
  },
  "goals": { "home": 2, "away": 1 },
  "score": {
    "halftime": { "home": 1, "away": 0 },
    "fulltime": { "home": 2, "away": 1 },
    "extratime": null,
    "penalty": null
  },
  "events": [ /* EnhancedEvent objects; empty in staging */ ]
}
```

#### Enhanced Fields (found-footy Tracking)

**Activation fields** (set when fixture moves staging → active):

```json
{
  "_activated_at": "2025-12-26T14:50:00Z",
  "_last_activity": null,
  "_last_monitor": "2025-12-26T15:02:30Z",
  "_completion_count": 0,
  "_completion_complete": false,
  "_completion_first_seen": null,
  "_completed_at": "2025-12-26T16:45:00Z"
}
```

**File:** `archive/src/data/models.py:251-280` (FixtureFields constants)

### Event Schema (Enhanced Events)

Each fixture's `events` array contains **EnhancedEvent** objects.
Raw API events are augmented with tracking fields:

#### Raw Event Fields (from api-football)

```json
{
  "time": { "elapsed": 42, "extra": 3 },
  "team": { "id": 40, "name": "Liverpool" },
  "player": { "id": 90, "name": "Mohamed Salah" },
  "assist": { "id": 3000, "name": "Andy Robertson" },
  "type": "Goal",
  "detail": "Normal Goal",
  "comments": null
}
```

#### Enhanced Fields (found-footy Tracking)

**Identification:**

```json
{
  "_event_id": "1234567_40_90_Goal_1"
}
```

Format: `{fixture_id}_{team_id}_{player_id}_{type}_{sequence}`.
Stable across API mutations to time/name/extra field changes —
only player, team, type, sequence matter for identity.

**Monitor / debounce tracking** (workflow-ID-based):

```json
{
  "_monitor_workflows": ["monitor-workflow-123", "monitor-workflow-124", "monitor-workflow-125"],
  "_monitor_complete": false,
  "_first_seen": "2025-12-26T14:42:30Z"
}
```

`_monitor_complete` is set to `true` by TwitterWorkflow at its own
start (proves Twitter actually started running, not just attempted
to spawn).

**Download workflow tracking** (workflow-ID-based):

```json
{
  "_download_workflows": ["download1-...", "download2-...", "download10-..."],
  "_download_complete": false,
  "_download_completed_at": "2025-12-26T14:52:30Z"
}
```

Each DownloadWorkflow adds its own ID via `$addToSet` at workflow
START (before any downloads). `_download_complete` flips to true
when `len(_download_workflows) >= 10`.

**Video storage:**

```json
{
  "_discovered_videos": [
    {
      "video_page_url": "https://twitter.com/...",
      "video_url": "https://pbs.twimg.com/...",
      "tweet_url": "https://twitter.com/.../status/123",
      "tweet_text": "SALAH!",
      "username": "skysports",
      "views": 50000, "likes": 10000, "retweets": 5000
    }
  ],
  "_s3_videos": [
    {
      "url": "/video/footy-videos/1234567_a1b2c3d4.mp4",
      "_s3_key": "1234567_a1b2c3d4.mp4",
      "perceptual_hash": "dense:0.25:...",
      "resolution_score": 0.95,
      "file_size": 12345678,
      "popularity": 3,
      "rank": 1,
      "width": 1920, "height": 1080,
      "aspect_ratio": 1.777,
      "bitrate": 5000000,
      "duration": 45.5,
      "source_url": "https://twitter.com/.../status/123",
      "hash_version": "dense:0.25",
      "timestamp_verified": true,
      "extracted_minute": 42,
      "timestamp_status": "verified"
    }
  ],
  "_video_count": 2
}
```

**Download stats** (Phase 1 telemetry):

```json
{
  "_download_stats": {
    "discovered": 15,
    "downloaded": 10,
    "filtered_aspect_duration": 2,
    "download_failed": 3,
    "md5_batch_deduped": 2,
    "md5_s3_matched": 1,
    "ai_rejected": 1,
    "ai_validation_failed": 0,
    "hash_generated": 6,
    "hash_failed": 1,
    "sent_to_upload": 5,
    "timestamp_rejected": 1,
    "uploaded": 3
  }
}
```

**VAR / drop tracking:**

```json
{
  "_drop_workflows": ["monitor-workflow-128", "monitor-workflow-129", "monitor-workflow-130"],
  "_removed": false
}
```

**Score context:**

```json
{
  "_score_after": "2-1",
  "_scoring_team": "home"
}
```

**Telemetry** (per-event SLO tracking):

```json
{
  "_telemetry": {
    "search_attempts": 10,
    "videos_discovered": 15,
    "videos_downloaded": 10,
    "videos_validated": 8,
    "videos_uploaded_to_s3": 3,
    "download_failures_by_class": {
      "VideoGeoRestrictedError": 2,
      "VideoNotAvailableError": 1
    },
    "llm_failures": 0,
    "first_seen_at": "2025-12-26T14:42:30Z",
    "first_s3_upload_at": "2025-12-26T14:52:30Z"
  }
}
```

**File:** `archive/src/data/models.py:282-408` (EventFields and EnhancedEvent)

### Team Aliases Collection

Document structure (cached at ingest time via RAG pipeline):

```json
{
  "_id": 40,
  "team_name": "Liverpool",
  "team_type": "club",
  "national": false,
  "country": "England",
  "city": "Liverpool",
  "twitter_aliases": ["LFC", "Reds", "Anfield", "Liverpool", "Merseyside"],
  "model": "qwen3-vl:8b-instruct",
  "wikidata_qid": "Q1130849",
  "wikidata_aliases": ["Liverpool F.C.", "Liverpool Football Club", "LFC"],
  "created_at": "2025-12-26T10:30:00Z",
  "updated_at": "2025-12-26T10:30:00Z"
}
```

The `twitter_aliases` array is what's used in Twitter searches.
Generated via:
1. Fetch aliases from Wikidata SPARQL
2. Preprocess to single words
3. LLM selects best words for Twitter search
4. Merge with team name + nationality adjectives

**File:** `archive/src/data/models.py:815-904` (TeamAlias constants)

### Cross-Collection Relationships

**Active Fixture** → **Events** (1:N)
- Each fixture has 0 or more enhanced events
- Events are stored as an array within the fixture document

**Event** → **S3 Videos** (1:N)
- Each event may have 1+ videos in `_s3_videos`
- Videos are ranked by quality signals (see §7)

**Fixture** → **Team Aliases** (1:1 per team, 2 total per fixture)
- Lookup: `team_aliases.find_one({_id: fixture.teams.home.id})`

---

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
to completed. See [§8](#8-fixture-completion-behavior) for full
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

## 5. Twitter Search / Discovery Behavior

### Trigger

Spawned by **MonitorWorkflow** as a fire-and-forget child workflow
when:
- `len(_monitor_workflows) >= 3` AND `_monitor_complete == false`
- WorkflowID: `twitter-{event_id}` (stable, enables dedup)
- Parent close policy: **ABANDON** (Twitter continues even if
  Monitor dies)
- ID reuse policy: **REJECT_DUPLICATE** (Temporal server enforces
  one TwitterWorkflow per event)

**File:** `archive/src/workflows/monitor_workflow.py:184-220`

### Alias Resolution (Cache vs. RAG)

TwitterWorkflow's first step is to resolve team aliases:

1. **Try cache lookup** (~30s timeout):
   - Query `team_aliases.find_one({_id: team_id})`
   - If found: use `twitter_aliases` array, proceed to search

2. **Cache miss:** Run full RAG pipeline (~30-90s):
   - Fetch Wikidata SPARQL query for team
   - Extract aliases, preprocess to single words
   - Call LLM (Qwen3-VL on joi) to select best Twitter words
   - Merge with team name + nationality adjectives
   - Save to `team_aliases` collection

3. **RAG fails:** Fallback to `[team_name]` (single word), log
   warning, continue

**File:** `archive/src/workflows/twitter_workflow.py:163-230`

### Search Cascade (15 Attempts, 10 Registrations Target)

TwitterWorkflow uses a **WHILE loop** (not a fixed FOR loop):

```
attempt = 0
while attempt < 15:  // Max 15 attempts
  attempt += 1

  download_count = get_download_workflow_count()
  if download_count >= 10:
    break  // Got 10 downloads, done!

  existing_urls = get_existing_video_urls()  // Dedup exclusion

  query = build_search_query(player_names, team_aliases)  // OR logic
  videos = execute_twitter_search(query, existing_urls, max_age=3min)

  save_discovered_videos(videos)

  download_workflow_id = f"download{attempt}-..."
  spawn_download_workflow(videos)  // Fire-and-forget

  wait(60 seconds)  // START-to-START spacing
```

**Key behaviors:**
- Exits when **10 DownloadWorkflows have registered** (count tracked
  via `_download_workflows` array)
- Safety cap: **15 max attempts** (prevents infinite loops)
- Spacing: **60 seconds between attempts** (START-to-START, so slow
  searches don't push later attempts late)
- **Always spawns DownloadWorkflow**, even with 0 videos
  (DownloadWorkflow registers itself regardless, so failed searches
  still increment the count)
- **Fire-and-forget**: TwitterWorkflow doesn't wait for download to
  complete; moves to next attempt immediately

**File:** `archive/src/workflows/twitter_workflow.py:249-517`

### Search Query Construction

For each attempt, TwitterWorkflow builds a query with **OR
operators**:

```
player_or = "Florian"  // or "(Florian OR Wirtz)" if multiple player names
team_or = "LFC"        // or "(LFC OR Liverpool OR Reds)" if multiple aliases

query = f"{player_or} {team_or}"
// Examples:
// "Florian (LFC OR Liverpool)"
// "(Florian OR Wirtz) Liverpool"
// "Mohamed Salah (LFC OR Reds OR Anfield)"
```

**File:** `archive/src/workflows/twitter_workflow.py:365-384`

### Twitter Search Mechanism (Browser Automation)

TwitterWorkflow calls `execute_twitter_search(query, existing_urls,
max_age_minutes=3)`, which:

1. **POSTs to Firefox automation service** (Selenium-based browser
   automation, separate Docker container)
2. **Selects tweets** matching the query from Twitter's "Latest" tab
3. **Extracts video metadata** (URL, duration, view count, likes,
   retweets)
4. **Chronological early-exit:** Stops scrolling when finding a
   tweet older than `max_age_minutes` (default 3 min)
5. **Deduplication by URL:** Skips videos already in `existing_urls`
6. **Adaptive early-exit:** If 2 consecutive scroll iterations
   return 0 new videos, stop scrolling

Returns **ALL videos found** (not limited at query time), sorted by
duration descending. TwitterWorkflow then picks top **5 longest
videos** to pass to DownloadWorkflow.

**Only video-bearing tweets are persisted.** Non-video tweets are
discarded on-the-fly during scrolling. There is no `_seen_tweets`
array — only `_discovered_videos`.

**File:** `archive/src/activities/twitter.py:243-350`

### Event Existence Check (VAR Termination)

At the start of each attempt, TwitterWorkflow checks if the event
still exists:

```
if not check_event_exists(fixture_id, event_id):
  log "Event deleted (VAR'd) - terminating early"
  return {...terminated_early: True...}
```

**File:** `archive/src/workflows/twitter_workflow.py:289-325`

### Monitor Complete Flag

TwitterWorkflow sets `_monitor_complete = true` **at the VERY
START** (before alias lookup, search, or anything else). This proves
to the system that TwitterWorkflow actually started running (not
just attempted to spawn). If TwitterWorkflow never runs (Temporal
issue), `_monitor_complete` stays false, and MonitorWorkflow will
retry spawning it.

**File:** `archive/src/workflows/twitter_workflow.py:143-161`

### Video Persistence (Discovered Videos)

TwitterWorkflow **saves ONLY the videos it's actually attempting to
download** to `_discovered_videos`:

```
save_discovered_videos(fixture_id, event_id, videos_to_download)
```

Videos found in earlier attempts are **not re-searched** (via
`get_twitter_search_data()`), but videos skipped due to
`max_videos_per_attempt = 5` limit are **not saved** (and may be
searched again in later attempts).

This is a **pragmatic tradeoff**: keeps video metadata bounded while
ensuring we don't re-search known videos.

**File:** `archive/src/workflows/twitter_workflow.py:448-469`

### Completed State

TwitterWorkflow **terminates when:**
1. **Download count reaches 10** (all 10 DownloadWorkflows have
   registered), OR
2. **Max attempts reached (15)**, OR
3. **Event was deleted** (VAR check failed)

It **does NOT set `_download_complete`** — that's set by
UploadWorkflow (or DownloadWorkflow on exit as failsafe, see §6).

Returns summary dict with total videos found, download count, exit
reason.

**File:** `archive/src/workflows/twitter_workflow.py:519-560`

---

## 6. Download / Video Validation Behavior

### Trigger

Spawned by **TwitterWorkflow** as fire-and-forget child workflow:
- **WorkflowID:** `download{attempt}-{team_clean}-{player_search}-{event_id}`
- **Parent close policy:** ABANDON (continues if parent dies)
- **ID reuse policy:** REJECT_DUPLICATE

Receives video list from TwitterWorkflow (0-5 videos).

**File:** `archive/src/workflows/twitter_workflow.py:479-506`

### Registration (Proof of Startup)

**First thing DownloadWorkflow does** (before any download):

```
register_download_workflow(fixture_id, event_id, workflow_id)
```

This adds the workflow ID to `_download_workflows` via `$addToSet`
(idempotent). If registration fails, the workflow doesn't proceed
but it **doesn't crash**; TwitterWorkflow will see count stay low
and retry on next attempt.

**File:** `archive/src/workflows/download_workflow.py:127-154`

### Download Pipeline

Pipeline stages (in order):

1. **Download videos** (parallel, per-video retry)
2. **MD5 batch dedup** (within this batch only)
3. **AI validation** (only MD5-unique videos)
4. **Perceptual hash generation** (only validated videos)
5. **Filter out videos without valid hash**
6. **Signal UploadWorkflow** (or skip if no videos)
7. **Cleanup** (if no videos to upload)
8. **Check and mark download complete** (finally block — runs on
   all exit paths)

**File:** `archive/src/workflows/download_workflow.py:97-633`

### Download Videos (Parallel)

For each video, DownloadWorkflow spawns an `asyncio` task:

```python
async def download_video(idx, video):
  try:
    result = execute_activity(
      download_activities.download_single_video,
      args=[video_url, idx, event_id, temp_dir, source_url],
      start_to_close_timeout=90s,
      retry_policy=RetryPolicy(max_attempts=3, backoff=2x)
    )
    return result
  except Exception:
    return {status: "failed", error: "..."}
```

**Temp directory:** `/tmp/found-footy/{event_id}_{run_id}` (run_id
from workflow instance, ensures uniqueness across replicas)

**Failure modes** (per-video, independent):
- **Video download error (403, timeout, etc.)** → logged, file
  deleted, count incremented
- **Filtered videos** (aspect ratio wrong, too short/long) → counted
  separately, file deleted
- **Multi-video tweets** → flattened into results (treated as N
  separate videos)

**File:** `archive/src/workflows/download_workflow.py:205-308`

### MD5 Batch Deduplication

After all downloads, DownloadWorkflow groups successful videos by
MD5 hash:

```
groups = {}
for video in successful:
  hash = video.file_hash
  if hash not in groups:
    groups[hash] = []
  groups[hash].append(video)

# For each group with multiple videos (MD5 duplicates):
for group in groups.values():
  if len(group) > 1:
    winner = max(group, key=lambda v: (v.resolution_score, v.file_size))
    winner.popularity = len(group)  # All copies contribute to popularity
    delete other files
```

This catches cases where Twitter has the exact same video file
linked from multiple tweets.

**Important:** This is **batch-level only**. S3 MD5 matching is
handled by UploadWorkflow (see §7).

**File:** `archive/src/workflows/download_workflow.py:310-344`

### AI Validation (Soccer Detection)

For each MD5-unique video, DownloadWorkflow calls:

```
validate_video_is_soccer(file_path, event_id, event_minute, event_extra)
```

This sends the video to **joi's Qwen3-VL model** with a structured
prompt requesting a JSON response:

- **soccer** (bool): Is this soccer footage?
- **screen** (bool): Is this a screen recording?
- **clock** (string): Extracted clock reading (e.g., "42:15")
- **added** (string): Stoppage minutes
- **stoppage_clock** (string): Stoppage-adjusted clock

**Timestamp verification:**
- If clock visible: extract OCR'd minute
- Compare to API elapsed minute (±1 minute tolerance to handle stoppage)
- If match: set `timestamp_verified = true`
- If mismatch: reject video (discard)

**Structured output:** Video is classified into:
- ✓ **Valid & verified** (soccer + clock matches)
- ✓ **Valid & unverified** (soccer + no clock / unverified clock)
- ✗ **Rejected** (not soccer, or wrong timestamp, or screen recording)

**Smart 2-3 check strategy:** Frames sampled at 25% and 75% of video
duration. If both agree, done (2 checks). If disagree, tiebreaker at
50% (3 checks). Activity heartbeat at each check to prevent Temporal
timeout.

**Concurrency:** Semaphore-limited to `LLM_CONCURRENCY_PER_WORKER=2`
in-flight requests per worker (matches joi's max_parallel=2 cap).

**Retry policy:** 4 attempts with exponential backoff (3s → 30s max).
If validation fails after retries, video is **FAIL-CLOSED**
(rejected, deleted).

**File:** `archive/src/workflows/download_workflow.py:345-439`

### Perceptual Hash Generation

For each validated video, DownloadWorkflow generates a **perceptual
hash** (dHash, dense sampling at 0.25s intervals):

```
generate_video_hash(file_path, duration)
  -> {perceptual_hash: "dense:0.25:t1=h1,t2=h2,...", ...}
```

**Algorithm** (dHash with histogram equalization):
- Dense sampling: frames every 0.25s
- Resize to 9×8 grayscale, apply `ImageOps.equalize()`
- Compare adjacent pixels → 64-bit hash per frame
- Format: `"dense:0.25:<ts1>=<hash1>,<ts2>=<hash2>,..."`

Used downstream for deduplication (Hamming distance < threshold =
duplicate).

**Parallel generation** (all at once, with heartbeat every 5 frames
for progress signaling).

**File:** `archive/src/workflows/download_workflow.py:440-505`

### Signal UploadWorkflow

After all validation, DownloadWorkflow **always signals
UploadWorkflow** (even if 0 videos):

```
queue_videos_for_upload(
  fixture_id, event_id,
  player_name, team_name,
  videos_to_upload,  // May be empty
  temp_dir,
  failures_by_class  // Phase 1 telemetry
)
```

This activity uses Temporal client's **signal-with-start pattern**:
- If no UploadWorkflow exists yet: **start one AND deliver signal in
  same call**
- If one exists: **enqueue the signal to the FIFO queue**

Signal name: `"add_videos"`. ID reuse policy: `ALLOW_DUPLICATE`
(handles late batches after a prior UploadWorkflow completed).

Temporal guarantees **signal ordering** = FIFO per event.

**File:** `archive/src/workflows/download_workflow.py:580-632`

### Completion Check (Finally Block)

DownloadWorkflow has a **try/finally block**. On exit (success,
exception, early return), it runs:

```
finally:
  check_and_mark_download_complete(fixture_id, event_id, threshold=10)
```

This activity:
- Counts entries in `_download_workflows` array
- If count >= 10: sets `_download_complete = true` (atomically,
  guarded by `$ne` to prevent concurrent races)
- If count < 10: no-op

This is **idempotent** — multiple DLWFs running this concurrently is
safe.

**Failsafe:** If every DLWF dies before reaching its finally block,
UploadWorkflow has an idle-timeout check that also runs this.

**File:** `archive/src/workflows/download_workflow.py:558-578`

---

## 7. Upload / Asset Persistence Behavior

### Trigger

Spawned by `queue_videos_for_upload()` activity (called from
DownloadWorkflow) using **signal-with-start pattern**:

- **First signal:** Starts UploadWorkflow with `UploadWorkflowInput`
  containing empty `videos` list
- **Subsequent signals:** Enqueued to FIFO signal queue (no new
  workflow spawn)
- **WorkflowID:** `upload-{event_id}` (one per event)

UploadWorkflow stays alive and processes batches until **idle
timeout** (5 minutes of no new signals).

**File:** `archive/src/workflows/upload_workflow.py:80-110`

### Signal Handling (FIFO Queue)

UploadWorkflow defines a signal handler:

```python
@workflow.signal
def add_videos(batch):
  self._pending_batches.append(batch)
```

Temporal guarantees signal **delivery order** = FIFO. UploadWorkflow
processes one batch at a time (sequential, no race conditions).

**File:** `archive/src/workflows/upload_workflow.py:66-78`

### Batch Processing Pipeline

For each batch:

1. **Fetch fresh S3 state** (existing videos for this event)
2. **Check event exists** (VAR check — abort if deleted)
3. **MD5 dedup** (check against existing S3 videos)
4. **Perceptual hash dedup** (scoped by timestamp verification status)
5. **Decide outcomes per video:**
   - **Upload** (new, no S3 match)
   - **Replace** (S3 match, new is better quality)
   - **Bump popularity** (S3 match, S3 is better, keep existing)
6. **Upload to S3 (parallel)**
7. **Update MongoDB** (new videos + in-place updates)
8. **Recalculate ranks**
9. **Notify frontend**

**File:** `archive/src/workflows/upload_workflow.py:168-679`

### Fetch S3 State

UploadWorkflow calls `fetch_event_data(fixture_id, event_id)`.
Returns **fresh S3 video metadata** for this event from MongoDB
`_s3_videos` array. This is the key to eliminating race conditions:
fetch S3 state inside the **serialized UploadWorkflow**, so no other
upload can modify it concurrently.

**File:** `archive/src/workflows/upload_workflow.py:202-242`

### MD5 Deduplication (Against S3)

UploadWorkflow calls `deduplicate_by_md5(downloaded_files,
existing_s3_videos)`. Returns:

```
{
  unique_videos: [...],
  md5_duplicates_removed: int,
  s3_exact_matches: [{video, s3_video, new_popularity}, ...],
  s3_replacements: [{new_video, old_s3_video}, ...]
}
```

**MD5 exact matches** (identical files) against S3 are handled via:
- **Exact match, new is better quality:** Replace (reuse same S3 key)
- **Exact match, S3 is better/same:** Bump popularity (keep existing)

**File:** `archive/src/activities/upload/dedup.py:46-228`

### Perceptual Hash Deduplication (Scoped by Timestamp)

UploadWorkflow splits videos into **2 pools** based on timestamp
verification status:

```
verified_videos = [v for v in candidates if v.timestamp_verified]
unverified_videos = [v for v in candidates if not v.timestamp_verified]

verified_s3 = [v for v in existing if v.timestamp_verified]
unverified_s3 = [v for v in existing if not v.timestamp_verified]

# Run dedup in parallel, verified vs verified, unverified vs unverified
verified_result = deduplicate_videos(verified_videos, verified_s3)
unverified_result = deduplicate_videos(unverified_videos, unverified_s3)
```

**Why scoped?** Prevents a verified goal clip from being replaced by
an unverified clip of a different broadcast moment (same match
timestamp = similar perceptual hashes).

**File:** `archive/src/workflows/upload_workflow.py:320-357`

### Perceptual Deduplication Algorithm

`deduplicate_videos()` uses **union-find clustering**:

1. **Batch clustering:** Compare each new video against all previous
   videos by perceptual hash
   - Match found: add to existing cluster
   - No match: create new cluster
2. **For each cluster:**
   - If 1 video: keep as-is
   - If N videos: **pick best using duration-aware selection**
     - If durations within 15%: prefer larger file (higher resolution)
     - If durations differ >15%: prefer longer (more complete clip)
     - Accumulate popularity from all cluster members to winner
3. **Batch winners vs. S3:**
   - If S3 match found: decide replace or skip (same duration logic)
   - If no S3 match: mark for upload

**File:** `archive/src/activities/upload/dedup.py:230-477`

### BUG? Multi-Match Corpus Dedup

At `archive/src/activities/upload/dedup.py:415-420`, the loop
searching for existing S3 videos to match a new candidate against
the corpus does:

```python
matched_existing = None
for existing in existing_videos_list:
    existing_hash = existing.get("perceptual_hash", "")
    if existing_hash and _perceptual_hashes_match(perceptual_hash, existing_hash):
        matched_existing = existing
        break  # ← STOPS AFTER FIRST MATCH
```

**Observed behavior**: if a new video is perceptually similar to N
existing S3 videos, only the FIRST match is processed. The other
N-1 stay in S3 as duplicates. `_old_s3_key` is only set for one
existing video, so upload replacement only touches one S3 key.

This is documented as behavior; the Go rewrite should decide whether
to preserve or fix.

### Three Outcomes Per Video

After dedup, each video is classified:

| Outcome | Action | S3 State |
| --- | --- | --- |
| **Upload** | New video → S3 PUT, add to `_s3_videos` | New entry |
| **Replace** | Upload new, keep same S3 key, in-place update metadata | Update existing |
| **Bump** | Keep existing, increment popularity, delete local file | Update popularity only |

**File:** `archive/src/workflows/upload_workflow.py:375-407`

### S3 Upload (Parallel)

For all videos marked upload/replace, `upload_single_video` is
called in parallel per video.

**S3 key format:** `{event_id}_{md5[:8]}.mp4`

**S3 URL:** `/video/footy-videos/{fixture_id}/{event_id}/{s3_key}`

**File metadata stored in S3 object tags** (via MinIO API, not
visible in object body).

**Retry policy:** 3 attempts with 1.5x backoff (2s, 3s, 4.5s).

**File:** `archive/src/workflows/upload_workflow.py:466-519`

### Scoring / Ranking System

`recalculate_video_ranks(fixture_id, event_id)` re-ranks
`_s3_videos` after each batch completes.

**Sort key:** `(timestamp_verified, popularity, file_size)` DESC.

Signal definitions:
- **`timestamp_verified`** (bool): Set by AI validation when the
  extracted clock matches the API's elapsed minute (±1 min). Primary
  tiebreaker.
- **`popularity`**: Count of duplicate sources found (how many
  Twitter accounts posted the same clip). Proxy for "the real clip"
  — many people posting it = probably real broadcast.
- **`file_size`**: Bytes. Proxy for resolution / quality.

No engagement metrics (views, likes, retweets) feed the score
today. No AI quality scoring beyond validation.

**Storage:** `rank` field (integer, 1..N) on each video object in
`_s3_videos` array.

**Winner determination** (used by fixture completion):
`monitor.py:417` checks `winner_exists =
completion_status["winner_exists"]` — checks if any video exists in
event's `_s3_videos` array. Rank #1 = winner.

**File:** `archive/src/data/videos.py:164-231`

### MongoDB Updates

**New videos:**

```
save_video_objects(fixture_id, event_id, video_objects)
  -> push to _s3_videos array
```

**Replacements (in-place):**

```
for replacement in replacements:
  update_video_in_place(
    fixture_id, event_id,
    s3_url,  # Same URL
    new_metadata  # Updated resolution_score, file_size, duration, hash, etc.
  )
  -> updates matching document in _s3_videos in-place
```

This avoids a race condition where video disappears between remove
+ add.

**File:** `archive/src/workflows/upload_workflow.py:556-634`

### Idle Timeout Failsafe

If UploadWorkflow gets **5 minutes with no new signals** (default
`UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES=5`), it exits and runs:

```
check_and_mark_download_complete(fixture_id, event_id, threshold=10)
```

This is a **failsafe** in case every DownloadWorkflow crashes
before reaching its finally block.

**File:** `archive/src/workflows/upload_workflow.py:94-109`

### Cleanup (File Level)

After successful S3 uploads, UploadWorkflow deletes the
**individual files** that were uploaded (not the entire temp
directory):

```
cleanup_individual_files(files_to_delete)
```

Temp directory is cleaned up by **MonitorWorkflow** after fixture
completes.

**File:** `archive/src/workflows/upload_workflow.py:681-712`

---

## 8. Fixture Completion Behavior

### Preconditions

Fixture can move from **fixtures_active → fixtures_completed** only
when:

1. **Status is completed** (FT, AET, PEN, CANC, ABD, WO, AWD)
2. **All valid events** have `_monitor_complete == true` AND
   `_download_complete == true`
3. **Completion counter** has reached threshold:
   - First poll with completed status: `_completion_count = 1`
   - Subsequent polls: increment (capped at 3)
   - Move when `count >= 3` **OR** winner data exists
     (`teams.home.winner` or `teams.away.winner` is not null)

**File:** `archive/src/activities/monitor.py:366-482`
(complete_fixture_if_ready)

### Completion Detection

MonitorWorkflow checks for completion after processing all events:

```python
if fixture_status in completed_statuses:
  was_completed = complete_fixture_if_ready(fixture_id)
  if was_completed:
    # Log match summary telemetry
    # Cleanup temp directories
```

The **3-poll debounce** handles API delays in confirming
winner/score finalization (especially important for penalty
shootouts).

### Match Completion Summary

When fixture completes, activity logs a **per-match telemetry
summary**:

```
{
  fixture_id, league_id, league_name,
  home_team, away_team, score_home, score_away,
  goals_total,
  videos_captured_total,
  coverage_rate,
  goals_with_zero_videos,
  top_failure_class,
  failure_classes_total,
  search_attempts_total,
  time_to_first_s3_p50_s
}
```

Logged with action `"match_completed_summary"`.

**File:** `archive/src/activities/monitor.py:49-144`

### Coverage SLO Check

For fixtures in tracked leagues (top-5 + UEFA + continental), if
coverage rate < 50%, a **separate warning log line** is emitted
(alertable in Grafana):

```
log.warning("match_below_slo",
  slo_threshold=0.5, coverage_rate=0.33, ...)
```

**File:** `archive/src/activities/monitor.py:452-472`

### State Transition

Fixture document is moved to **fixtures_completed** with:
- All fields from fixtures_active preserved
- New field: `_completed_at = now`
- All events have `_monitor_complete` and `_download_complete` true

### Temp Directory Cleanup

MonitorWorkflow then cleans up temp directories for the completed
fixture:

```
cleanup_fixture_temp_dirs(fixture_id)
  -> removes /tmp/found-footy/* files for this fixture
```

**File:** `archive/src/workflows/monitor_workflow.py:269-283`

---

## 9. Cross-Workflow Coordination

### Coordination Mechanisms

| From | To | Mechanism | Wait? | Notes |
| --- | --- | --- | --- | --- |
| **Ingest** | **RAG** | `workflow.execute_activity()` | Yes | Per-team, blocking, 2 retries max |
| **Monitor** | **Twitter** | `start_child_workflow()` | No | Fire-and-forget, REJECT_DUPLICATE |
| **Twitter** | **Download** | `start_child_workflow()` | No | Fire-and-forget, 1 per attempt (up to 15) |
| **Download** | **Upload** | `signal_with_start()` | No | FIFO queue via Temporal signals |
| **Monitor** | **Upload** | `check_and_mark_download_complete()` | No | Failsafe, idle-timeout backup |
| Everything | **Frontend** | `notify_frontend_refresh()` | No | Best-effort SSE broadcast |

### Ingest → RAG

**Synchronous, per-team:**
- `get_team_aliases(team_id, team_name, team_type, country)`
- Blocks ingest workflow until result (cache hit or RAG complete)
- Retry: 2 attempts max
- Timeout: 90 seconds
- Failure: logs warning, continues

**File:** `archive/src/workflows/ingest_workflow.py:288-304`

### Monitor → Twitter

**Fire-and-forget child workflow:**
- Spawned when `len(_monitor_workflows) >= 3 AND _monitor_complete == false`
- MonitorWorkflow doesn't wait for TwitterWorkflow to complete
- Temporal `REJECT_DUPLICATE` prevents accidental re-spawning same event
- Parent close policy `ABANDON`: TwitterWorkflow continues even if
  Monitor dies

**File:** `archive/src/workflows/monitor_workflow.py:184-220`

### Twitter → Download

**Fire-and-forget child workflow (×10):**
- One DownloadWorkflow spawned per Twitter attempt (up to 15 total)
- TwitterWorkflow doesn't wait for download to complete
- Parent close policy `ABANDON`
- DownloadWorkflow registers itself at START (adds to
  `_download_workflows` count)

**File:** `archive/src/workflows/twitter_workflow.py:479-506`

### Download → Upload (Signal-with-Start)

**Async signal delivery, FIFO queue:**
- DownloadWorkflow calls `queue_videos_for_upload()` activity
- Activity uses Temporal client to signal-with-start UploadWorkflow
- If no UploadWorkflow exists: starts one, immediately delivers signal
- If exists: enqueues signal to `_pending_batches` deque
- Temporal guarantees signal **delivery order** (FIFO)
- UploadWorkflow processes signals **one at a time** (sequential,
  no parallelism per event)

**File:** `archive/src/workflows/download_workflow.py:580-632`

### Monitor → Upload (Failsafe)

**Idle-timeout backup:**
- UploadWorkflow waits up to 5 minutes for new signals
- On timeout, runs `check_and_mark_download_complete()` as failsafe

**File:** `archive/src/workflows/upload_workflow.py:94-109`

### Everything → Frontend (SSE Broadcast)

**Best-effort notification:**
- Called by Ingest, Monitor (if events triggered), Twitter (on
  completion)
- Posts to `/api/found-footy/refresh` (legacy vedanta-systems endpoint)
- Also posts to `/api/v1/internal/notify` (new found-footy FastAPI endpoint)
- Both paths run unconditionally; failure of either is logged but
  doesn't fail activity

**File:** `archive/src/activities/monitor.py:764-820`

---

## 10. Failure Modes & Recovery

### Twitter Authentication Expiry

**Symptom:** Twitter service returns 503 "Login Required" or 401
"Unauthorized"

**Recovery path:**
1. Selenium/Firefox **refreshes cookies** (via session persistence, ~30s)
2. If refresh fails: **VNC intervention** required (manual re-login
   to Twitter via VNC interface)
3. Workflow continues after auth restored
4. Current attempt may be lost, but TwitterWorkflow retries on next attempt

### LLM Unavailability (joi Down)

**Symptom:** `validate_video_is_soccer()` times out or returns 5xx

**Recovery:**
- Activity has **4 retry attempts** with exponential backoff (3s → 30s max)
- If all 4 fail: video is **FAIL-CLOSED** (rejected, deleted)
- DownloadWorkflow continues (per-video failures don't block batch)
- Telemetry recorded in `_download_stats.ai_validation_failed`

### Video Geo-Restriction (yt-dlp 403)

**Symptom:** `download_single_video()` raises `VideoGeoRestrictedError`

**Handled:** Video marked as failed, counted in
`_download_stats.download_failed`, file deleted. DownloadWorkflow
continues.

### S3 Upload Failure

**Symptom:** `upload_single_video()` fails to PUT object

**Recovery:**
- Activity has **3 retry attempts** with 1.5x backoff (2s, 3s, 4.5s)
- If all 3 fail: video marked as upload failure
- UploadWorkflow continues processing other videos in batch

### MongoDB Write Failure

**Recovery:** Temporal activity retries (2-3 attempts, varies by
activity). If retry exhausted: activity raises, workflow may crash
or be retried by Temporal.

### Twitter Workflow Crash (Mid-Search)

**Recovery:**
- `_download_workflows` array still contains IDs of started downloads
- MonitorWorkflow eventually sees `_monitor_complete = true`, but
  `_download_complete = false`
- Next monitor cycle: event stays visible, Monitor doesn't re-trigger
  Twitter (already started once)
- DownloadWorkflows continue running independently
- If fewer than 10 downloads finish: event gets stuck at
  `_download_complete = false` indefinitely
  - **Workaround:** Manual fixture re-ingest or emergency re-trigger

### Download Workflow Crash (Mid-Download)

**Recovery:**
- Workflow ID is already in `_download_workflows` (count incremented
  at START)
- TwitterWorkflow sees count >= 10 → exits loop
- No videos uploaded (DLWF crashed before signaling UploadWorkflow)
- Event has `_download_complete = true` but 0 videos in S3

### Upload Workflow Crash (Mid-Batch)

**Recovery:**
- Uploaded videos are **already in S3** (PUT succeeded)
- MongoDB `_s3_videos` array is **not updated** (crash before write)
- Fixture shows 0 videos to frontend, but blobs exist in S3
- **Workaround:** Manual MongoDB update or re-trigger upload

### Concurrent Event Modifications (Race Condition)

**Behavior:**
- Both fetch active fixture from MongoDB
- Both attempt to update same event
- Last write wins (MongoDB is last-write-wins, not ACID)

### Duplicate Signal Delivery

**Behavior:**
- Signal handler appends to `_pending_batches` deque each time
- Same batch processed twice
- Videos deduplicated against S3 → second pass bumps popularity

**Outcome:** Benign, no data corruption.

---

## 11. Edge Cases & Corner Behaviors

### Late Goals (90+ minutes)

**Scenario:** Goal scored in 95th minute of match

**Behavior:**
- API returns goal with `elapsed = 95`
- Monitor detects event normally
- Twitter searches with player + team
- System works the same (no special halftime handling for late goals)
- All 15 attempts run regardless of clock time

### VAR Reversal Mid-Discovery

**Timeline:**
- T0: Goal detected, event added to active, monitor debounces
- T1: Twitter search spawned
- T2: 5 DownloadWorkflows have registered
- T3: API removes goal (VAR'd)
- T4: MonitorWorkflow polls again, event not in live

**Behavior:**
- VAR handler adds MonitorWorkflow ID to `_drop_workflows`
- After 3 drops: event **deleted entirely** (S3 videos deleted if
  `_monitor_complete = true`)
- In-flight DownloadWorkflows continue (they have video list cached)
- Remaining uploads to S3 happen, but event is gone → S3 orphaned
  videos

**Mitigation:** UploadWorkflow checks event exists before processing
batch; aborts if deleted.

### Postponement Mid-Monitoring (PST Status)

**Behavior:**
- Monitor sees status PST (treated as ACTIVE)
- Fixture stays in fixtures_active (not moved back to staging)
- Events already detected stay as-is
- If match resumes: Monitor continues polling, detects new goals
- If match cancelled: Status changes to CANC → completion check
  triggers → moves to fixtures_completed

### Simultaneous Events on Same Fixture

**Behavior:**
- Both have different `sequence` numbers
- Event IDs are distinct
- TwitterWorkflow spawned separately for each
- DownloadWorkflows run in parallel (different event IDs, different
  S3 keys)
- UploadWorkflows process separately (different UploadWorkflow IDs)

**Outcome:** Fully parallelized.

### Halftime Handling

**No special behavior:**
- Monitor treats HT status just like 1H or 2H
- Continues polling
- Events continue to be detected
- No "halftime reset" of counters or fields

Events do NOT disappear during halftime — they persist in the API's
events array across the break. Set-based comparison handles them
naturally.

### Event with Null player.id (Early API Updates)

**Behavior:**
- Event is created with `player_id = 0`
- `is_player_known()` returns **false** (0 treated as unknown)
- Event added to active but with `_monitor_workflows = []` (not registered)
- Event stays visible in frontend but won't debounce/trigger Twitter
- When API updates with actual player_id (seconds/minutes later), a
  **new event is created** (new sequence for known player)
- Old "Unknown" event is eventually VAR'd (seen missing for 3 polls)

**File:** `archive/src/activities/monitor.py:27-46`

### Event with Null player.name (But player.id Present)

**Behavior:** Same as above — `is_player_known()` requires both id
and name.

### Backdated Fixtures (Kickoff Already Passed at Ingest Time)

**Behavior:**
- Fixture would be in fixtures_staging (not started)
- At next `pre_activate_upcoming_fixtures` check, it's in the past
- Check: `fixture.date <= now + 30min` → **false**
- Fixture stays in staging, never activated

### Timezone Edge Cases (UTC-12 to UTC+14)

**Behavior:**
- API returns ISO datetime (docs say UTC)
- Assuming UTC: ingest fetches "today" UTC, which may miss today in
  UTC+14 or catch tomorrow in UTC-12

**Mitigation:** Three-day fetch (today/tomorrow/day-after) should
cover all edge cases.

---

## 12. Configuration Reference

All constants are in `archive/src/utils/orchestration_config.py`
unless noted.

### Monitor / Event Debounce

| Constant | Value | Purpose |
| --- | --- | --- |
| `MONITOR_DEBOUNCE_STABLE_COUNT` | 3 | Polls before event is trusted (NEW, REMOVED) |
| `MONITOR_DROP_THRESHOLD` | 3 | Polls before event is deleted (VAR) |
| `MONITOR_STAGING_LOOKAHEAD_MINUTES` | 30 | Pre-activate window |
| `MONITOR_STAGING_INTERVAL_MINUTES` | 15 | Staging polling interval |

### Twitter Discovery Loop

| Constant | Value | Purpose |
| --- | --- | --- |
| `TWITTER_MAX_ATTEMPTS` | 15 | Safety cap on search attempts |
| `TWITTER_REQUIRED_DOWNLOADS` | 10 | Target registration count to exit loop |
| `TWITTER_MAX_VIDEOS_PER_ATTEMPT` | 5 | Top N longest videos to download per attempt |
| `TWITTER_SEARCH_MAX_AGE_MINUTES` | 3 | Age filter for tweets (stop scrolling) |
| `TWITTER_ATTEMPT_SPACING_SECONDS` | 60 | START-to-START delay between attempts |
| `TWITTER_ATTEMPT_MIN_WAIT_SECONDS` | 10 | Minimum wait if attempt completes early |

### Upload Workflow

| Constant | Value | Purpose |
| --- | --- | --- |
| `UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES` | 5 | Signal queue timeout before exit |

### Phase 4 — Coverage SLO

| Constant | Value | Purpose |
| --- | --- | --- |
| `MATCH_COVERAGE_SLO_THRESHOLD` | 0.5 | Alert if coverage < 50% |
| `SLO_TRACKED_LEAGUE_IDS` | {39, 140, 78, 135, 61, 2, 3, 848} | Leagues with SLO |

### Canary (DOM Selector Validation)

| Constant | Value | Purpose |
| --- | --- | --- |
| `DOM_CANARY_QUERY` | "football goal" | Synthetic test query |
| `DOM_CANARY_MAX_AGE_MINUTES` | 1440 | 24-hour window |
| `DOM_CANARY_MIN_TWEETS` | 3 | Minimum results to pass canary |

### LLM Concurrency

| Constant | Value | Purpose |
| --- | --- | --- |
| `LLM_CONCURRENCY_PER_WORKER` | 2 | In-flight AI validation requests per worker |

### Event Configuration

| Module | Constant | Value | Purpose |
| --- | --- | --- | --- |
| `event_config.py` | `TRACKABLE_EVENT_TYPES` | ["Goal"] | Only these event types tracked |
| `event_config.py` | `TRACKABLE_GOAL_DETAILS` | ["Normal Goal", "Penalty", "Own Goal"] | Goal detail types tracked |

### Fixture Retention

| Constant | Value | Purpose |
| --- | --- | --- |
| `FIXTURE_RETENTION_DAYS` | 14 | Days to keep completed fixtures |

---

## 13. External Dependencies & Contracts

### API-Football

**Endpoints used:**
- `GET /fixtures` (batch by IDs) → `fixtures_batch(fixture_ids: List[int])`
- `GET /fixtures?date=YYYY-MM-DD` → `get_fixtures_for_date(date: str)`

**Expected fields:** Fixture, league, teams, goals, score, events
arrays with full player/assist metadata.

**Rate limiting:** Tier-dependent. No formal backoff/retry beyond
Temporal retries.

### Twitter (Browser Automation)

**Service:** Firefox Selenium-based automation running in separate
Docker container.

**Endpoints:**
- `POST /search` → `execute_twitter_search()`
- Expects: query string, existing_video_urls, max_age_minutes
- Returns: List of video metadata

**Fragility points:**
- Twitter DOM selectors can change (addressed by DOM canary test)
- Rate limiting / login required (VNC intervention)
- Browser may crash (restart handled by Docker health checks)

### Wikidata (SPARQL API)

**Used by:** RAG pipeline (get_team_aliases activity)

**Query:** SPARQL to fetch team aliases by QID.

### joi LLM (Qwen3-VL on llama.cpp)

**Endpoints:**
- `POST /predict` → `validate_video_is_soccer()`
- Input: video file path, event_minute, event_extra
- Output: JSON with is_soccer, is_screen_recording, clock_visible,
  extracted_minute

**Concurrency:** Semaphore-limited to 2 concurrent requests per
worker (`LLM_CONCURRENCY_PER_WORKER`).

### MinIO / S3

**Bucket:** footy-videos (or configured)

**Operations:**
- `PUT object` → `upload_single_video()`
- `GET object` (for metadata) → `deduplicate_videos()`

**Key format:** `{event_id}_{md5[:8]}.mp4`

**URL format:** `/video/footy-videos/{fixture_id}/{event_id}/{key}`

### MongoDB

**Collections:** fixtures_staging, fixtures_active, fixtures_live,
fixtures_completed, team_aliases, video_shares (legacy)

**Connection:** Single replica (no sharding, no HA)

**Transactions:** Not used (last-write-wins semantics)

### Temporal

**Workflow types:** IngestWorkflow, MonitorWorkflow, TwitterWorkflow,
DownloadWorkflow, UploadWorkflow

**Activity queues:** "found-footy" (single queue for all activities)

---

## 14. Retention & Cleanup Policies

### Fixtures

- Created: at ingest time
- Deleted: **14 days after completion**
- Calculation: `cleanup_old_fixtures(14)` runs at end of ingest,
  deletes fixtures with `fixture.date < today - 14 days`

### S3 Videos

- Lifetime: Tied to fixture retention
- Cleanup: When fixture is deleted, all S3 videos for that fixture
  are deleted
- Orphaned videos: If UploadWorkflow crashes after S3 PUT but before
  MongoDB write, videos become orphaned. No automatic cleanup.

### Events

- Lifetime: Tied to fixture (deleted when fixture deleted)
- Event deletion: Also happens immediately when VAR'd

### Team Aliases

- Lifetime: Session-persistent (or until manually refreshed)
- Refresh: Only when new team encountered at ingest time
- No TTL

### Twitter Cookies

- Lifetime: ~30 days (browser session)
- Persistence: Stored in Firefox profile (mounted volume)
- Refresh: Automatically refreshed by Selenium
- Expiry handling: VNC intervention if explicit re-login needed

### Event Telemetry

- Lifetime: Tied to event (deleted with fixture after 14 days)

### Monitor/Drop Workflows Array

- Lifetime: Tied to event (cleared if event reappears after VAR)
- Array growth: Unbounded per event

---

## 15. Observability & Telemetry

### Logging Strategy

All modules use centralized `log` module
(`archive/src/utils/footy_logging.py`) with structured fields:

```
log.info(logger, MODULE, action, message, **fields)
log.warning(logger, MODULE, action, message, **fields)
log.error(logger, MODULE, action, message, **fields)
```

**MODULE:** String identifying the workflow/activity (e.g.,
"monitor", "twitter", "ingest")

**action:** String identifying the event (e.g., "new_event",
"video_rejected", "completion_started")

All logs go to **Loki** (via structured logging) and **Grafana** for
alerting/dashboarding.

### Named Log Actions (Sampled)

#### Ingest Module

| Action | When | Key Fields |
| --- | --- | --- |
| `fetch_fixtures_started` | Ingest begins | target_date |
| `fixtures_retrieved` | API call returns | total |
| `fixtures_filtered` | Team filter applied | filtered, removed |
| `categorized` | Fixtures routed | total, staging, active, completed |
| `stored` | MongoDB inserts | staging, active, completed |
| `skipped_existing` | Duplicates detected | count |

#### Monitor Module

| Action | When | Key Fields |
| --- | --- | --- |
| `staging_poll` | Staging check starts | polling, total |
| `pre_activated` | Fixture moved to active | fixture_id, kickoff_in_minutes |
| `emergency_activation` | Game started in staging | fixture_id, status |
| `new_event` | Event detected | event_id, player_status |
| `monitoring` | Event tracking | event_id, count, max_count |
| `ready_for_twitter` | Event debounced | event_id, monitor_workflows |
| `var_removed` | Event deleted | event_id, drop_workflows |
| `match_completed_summary` | Fixture completes | goals_total, coverage_rate, failure_classes_total |
| `match_below_slo` | Coverage SLO triggered | league_name, coverage_rate, goals_total |

#### Twitter Module

| Action | When | Key Fields |
| --- | --- | --- |
| `started` | Workflow begins | event_id, team_id, player_names |
| `alias_cache_hit` | RAG cache hit | aliases |
| `rag_success` | RAG pipeline completed | aliases |
| `search_query` | Query built | query, excluding_count |
| `search_complete` | Twitter search returns | found, query |
| `attempt_search_complete` | Attempt finished | attempt, unique_videos |
| `download_count_reached` | Exit loop | download_count, event_id |
| `event_deleted` | VAR detected | event_id |
| `loop_complete` | TwitterWorkflow exits | reason, download_count, attempts |

#### Download Module

| Action | When | Key Fields |
| --- | --- | --- |
| `started` | Workflow begins | event_id, videos |
| `registered` | Self-registration | workflow_id, download_count, event_id |
| `downloads_complete` | Download stage done | success, filtered, failed |
| `batch_dedup_complete` | MD5 dedup | unique, batch_dupes |
| `validation_complete` | AI validation done | passed, rejected, validation_errors |
| `hash_generation_complete` | Perceptual hashing | generated, total |
| `workflow_complete` | Workflow exits | uploaded, s3_urls, **download_stats |

#### Upload Module

| Action | When | Key Fields |
| --- | --- | --- |
| `batch_received` | Signal delivered | videos, queue_size |
| `md5_dedup_complete` | MD5 dedup | unique, batch_dupes, s3_matches, s3_replacements |
| `perceptual_dedup_complete` | Perceptual dedup | new, replacements, skipped, verified_new, verified_replaced |
| `uploads_complete` | S3 uploads done | success, total |
| `saved_to_mongodb` | MongoDB update | count |
| `workflow_complete` | UploadWorkflow exits | total_uploaded, batches |

### Telemetry Emission Points

**Per-match summary** (logged at fixture completion):
- `match_completed_summary` log line (Loki JSON queryable)
- Fields: goals_total, videos_captured_total, coverage_rate,
  failure_classes_total, time_to_first_s3_p50_s

**Per-event telemetry** (stored in `_telemetry` field):
- search_attempts, videos_discovered, videos_validated,
  download_failures_by_class
- first_seen_at, first_s3_upload_at (for latency metrics)

**Per-download-workflow stats** (stored in `_download_stats` field):
- discovered, downloaded, md5_batch_deduped, ai_rejected,
  hash_generated, uploaded
- Breakdown by failure class

---

## 16. Gaps in This Spec

The following behaviors are **unclear from code** or
**undocumented**, and would need testing/clarification before Go
rewrite implementation:

1. **API-Football response format for fixtures on same day,
   different times** — does `get_fixtures_for_date()` return ALL
   fixtures for that UTC date? How are UTC-±N timezones handled?

2. **Duplicate event_id collision** — should there be a unique
   index on `(fixture_id, event_id)` within the events array?

3. **VAR reversal edge case: what if event reappears after 3-drop
   deletion?** Would sequence number reset or continue from before?

4. **Perceptual hash collision with different goals** — could two
   different goals have similar enough hashes to trigger false
   dedup? Hamming distance threshold is UNCLEAR (not in provided code).

5. **Timezone handling in ingest lookahead** — is 3-day lookahead
   sufficient to catch all timezones from UTC-12 to UTC+14?

6. **Twitter service discovery in production (TWITTER_SCALED)** —
   what if all 8 instances are unhealthy? Round-robin? Failover?

7. **Wikidata SPARQL rate limiting** — no documented rate limit
   handling. Retry or fail?

8. **MongoDB write throughput** — concurrent monitors + uploads +
   ingests all writing to fixtures_active. No formal concurrency
   control. Could lost updates happen?

9. **S3 object metadata (file size, duration, bitrate)** — where
   stored? S3 object tags? MongoDB metadata? Extracted at upload
   time or passed in?

10. **Temporal workflow replay semantics** — could old
    MonitorWorkflow IDs re-appear in replayed workflows?

11. **Event player.id = 0 handling (own goals)** — do own goals
    ever get triggered for Twitter, or do they get stuck at
    "unknown player"?

12. **Fixture status cancellation mid-ingest** — is CANC-during-
    ingest handled correctly?

13. **Download workflow failure classes** — full list of error
    classes not provided. Fallback for unknown errors?

14. **Alias cache invalidation** — no TTL on team_aliases. If team
    changes name, when is cache refreshed?

15. **Monitor cycle timing under load** — if processing takes 35s,
    does next cycle start immediately or wait?

---

## Summary

The Python found-footy system is a **real-time event-driven
pipeline** with clear data flow (Ingest → Monitor → Twitter →
Download → Upload) and robust failure handling through **Temporal
workflows**. The specification above captures **what the system
actually does** at the level of detail needed for a faithful Go
rewrite.

Key invariants:
- **One TwitterWorkflow per event** (stable ID, server-enforced dedup)
- **Workflow-ID-based tracking** (idempotent registration via arrays)
- **Serialized per-event uploads** (FIFO signal queue via Temporal)
- **Scoped deduplication** (verified videos only compared against
  verified S3)
- **3-poll debounce** (both event confirmation and VAR reversal)
- **14-day retention** (fixtures auto-expire, self-cleaning)
- **Fire-and-forget child workflows** (ABANDON policy, independent
  failure domains)

The system tolerates transient failures well (API glitches, network
timeouts, LLM unavailability) but has known edge cases around
concurrent modifications, orphaned S3 blobs, and unclear timezone
semantics. The `dedup.py:415-420` first-match-only bug is flagged as
`BUG?` for the Go rewrite to decide whether to preserve or fix.
