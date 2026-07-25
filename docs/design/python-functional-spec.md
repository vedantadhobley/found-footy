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

This spec **complements** [`rebuild-plan.md`](rebuild-plan.md),
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

---

# Detailed subsystem specs — 2026-07-18 addendum

The following per-subsystem specs were produced by parallel deep-read
agents on 2026-07-18 as ground-truth input for the Aug-15 rebuild
roadmap. Each is a **WHAT + WHY** behavior spec — describes what
Python does today and (crucially) why, so the Go rewrite can preserve
load-bearing behaviors without silently regressing them, and can
knowingly redesign the parts that are Python-era baggage. Line
references throughout cite `archive/`.

## Twitter service — Python behavior spec (WHAT + WHY)

Files referenced: `archive/twitter/{session.py,scrape.py,app.py,config.py,README.md}`.

### 1. Startup + dual-mode auth lifecycle

- **PURPOSE**: Boot the FastAPI surface immediately, then asynchronously establish an authenticated Firefox profile via a two-mode ladder (Selenium headless for scraping, non-Selenium Firefox for login) so Twitter's bot detection never sees Selenium during credential entry.
- **BEHAVIOR**:
  - FastAPI `lifespan` (`app.py:75-90`) launches `background_startup` on a daemon thread so `/health` responds even while auth is pending.
  - `startup()` (`session.py:705`) calls `ensure_authenticated()`, which tries: reuse live session → cookie backup restore → boot headless Firefox against the persisted profile → give up.
  - On give-up, `startup()` sends a one-shot email (`_send_login_notification`), launches non-Selenium Firefox via `subprocess.Popen` on `DISPLAY=:99` (`session.py:313-336`), and spawns a poll thread (`auto_verify_loop`, `session.py:732-756`) that watches `cookies.sqlite` mtime.
  - When the loop detects fresh mtime, it kills manual Firefox, re-boots Selenium headless, verifies logged-in, and backs cookies up.
  - `ensure_authenticated()` has a 60-second warm-path shortcut: if `last_activity` is recent, only a cheap `driver.current_url` check runs (`session.py:426-437`).
- **REMARKS**:
  - The singleton pattern (`__new__` with lock, `session.py:52-58`) means all endpoints/threads share one browser — no per-request isolation.
  - Manual Firefox and Selenium Firefox share the same `profile_dir`; they never coexist, but the killing (`pkill -9 firefox`, `session.py:316,341`) is process-wide and would nuke unrelated Firefoxes on the same host — safe only inside a dedicated container.
  - `authenticate()` is referenced by the `/authenticate` endpoint (`app.py:233`) but does not exist on `TwitterSessionManager`. That endpoint is dead code that raises `AttributeError` at call time — a rebuild should not port it.

### 2. Cookie persistence during active scraping

- **PURPOSE**: Keep the JSON backup file fresh so container restarts (or a sibling scaled instance) can resume without a manual re-login round-trip.
- **BEHAVIOR**:
  - Backup is event-driven, not timer-driven. It happens: after every successful `_do_search` (`session.py:691`), after cookie-restore succeeds (`session.py:470`), after Firefox-profile fallback succeeds (`session.py:480`), after post-manual-login verify succeeds (`session.py:749`, `769`).
  - `_backup_cookies_to_host` filters to `x.com` domain cookies, refuses to write if `auth_token` is missing (`session.py:293-296`), writes JSON `{exported_at, cookies}` atomically over the backup path.
  - The **only** mtime tracking anywhere is in `auto_verify_loop`, watching `profile_dir/cookies.sqlite` (`session.py:737-740`) — that's the trigger for detecting completion of manual login, not for scheduling backups.
  - No throttling, no debounce — a busy instance backs up on every search, which for the 30 s cycle means once per goal search per instance.
- **REMARKS**:
  - Backup path defaults to `~/.config/found-footy/twitter_cookies.json` but is overridden via `TWITTER_COOKIE_BACKUP_PATH` (`session.py:88-89`); scaled instances share one path via the host mount — race-safe only because writes are last-writer-wins, all writers hold identical auth cookies.
  - The `auth_token` guard is load-bearing: without it a partial cookie set could overwrite a good backup and brick every instance on next restart. Silent regression risk if a Go port drops this check.

### 3. Search API contract

- **PURPOSE**: One HTTP call to fan out a "find recent videos matching this query" browser scrape, returning a normalized list to the Temporal activity.
- **BEHAVIOR**:
  - `POST /search` (`app.py:173-216`) body: `{search_query: str, exclude_urls: list[str]=[], max_age_minutes: int=5}`.
  - Success response: `{status: "success", videos: [...], count: N}`. Each video dict carries `search_term, tweet_url, tweet_id, tweet_text[:200], username, timestamp, discovered_at, search_index (always 0), video_index, source="browser_automation", requires_ytdlp=True, video_page_url, duration_seconds, tweet_age_minutes` (`session.py:649-664`).
  - Errors: `503` with body `{error: "authentication_required", vnc_url, ...}` on `TwitterAuthError`; `500` bare `detail=str(e)` on anything else.
- **REMARKS**:
  - `requires_ytdlp=True` is a lie in the current stack — downloads go through `/download_video`, not yt-dlp. Rebuild should drop or repurpose.
  - `search_index=0` is a vestigial field; nothing in the response distinguishes attempt N from attempt 1 — the caller tracks that.
  - `exclude_urls` is normalized to a *tweet-ID set* (`session.py:526-530`), so callers can pass either `/user/status/…` or `/i/status/…` shapes interchangeably. Under-documented, load-bearing for cross-attempt dedup.

### 4. Scroll termination

- **PURPOSE**: Bound work so a search over a quiet query doesn't scroll forever, and end early when we've walked past the "recent enough" horizon.
- **BEHAVIOR**: Four stop conditions in `_do_search` (`session.py:590-684`):
  1. Any tweet's `tweet_age_minutes > max_age_minutes` sets `found_old_tweet=True` and breaks (`session.py:612-616`). Primary path.
  2. `scroll_count >= max_scrolls` (hardcoded 10) exits the while loop (`session.py:590`).
  3. `len(tweet_elements) == 0 and scroll_count >= 1` — empty results page after first scroll (`session.py:675-677`).
  4. Any exception on `driver.get` (page-load timeout of 30 s) raises `RuntimeError` all the way out (`session.py:557-572`).
- **REMARKS**:
  - The age check requires a parseable `<time datetime>` — if that element is missing or malformed, `tweet_age_minutes` is `None` and the "too old" branch never fires for that tweet.
  - No "found N videos, stop" cap. Callers rely on `max_age_minutes` alone to bound cost.
  - Inside the per-tweet loop, exceptions are swallowed with `continue` (`session.py:669-670`), losing the tweet silently. Load-bearing forgiveness — a rebuild that surfaces those errors would drown in noise.

### 5. DOM extraction fields (per tweet)

- **PURPOSE**: Reduce a tweet `article` element to the minimal record needed downstream, tolerating X's shifting markup by trying multiple selectors.
- **BEHAVIOR** (all in `scrape.py`):
  - `extract_status_link` — first `a[href*='/status/']` (`scrape.py:94-105`). Also the source of `tweet_url`.
  - `extract_tweet_age_minutes` — `time[datetime]` parsed ISO-8601, delta vs UTC now (`scrape.py:108-120`).
  - `is_promoted_tweet` — XPath text-contains for "Promoted"/"Ad" (`scrape.py:123-133`).
  - `extract_tweet_text` — `[data-testid='tweetText']`, defaults to `"Text not found"`, truncated to 200 chars (`scrape.py:136-145`).
  - `extract_video_duration` — walks selectors `video → [data-testid='videoPlayer'] → [data-testid='videoComponent']` (`scrape.py:175`), then for duration tries `video.duration` attribute first, then four overlay-text selectors (`scrape.py:192-197`) parsed as `M:SS`.
  - Post-processing in `_do_search`: `extract_tweet_id_from_url` splits `/status/` (`scrape.py:44-53`), `extract_username_from_url` from URL path (`scrape.py:56-72`) — returns `"Unknown"` for `/i/status/…`, `video_page_url` is built as `https://x.com/i/status/{tweet_id}` (`session.py:661`).
- **REMARKS**:
  - `is_truncated_snowflake` (`scrape.py:75-86`, `MIN_SNOWFLAKE_LEN=18`) filters IDs that look like X's upstream rendering quirks for deleted/quoted tweets — these won't syndicate to a downloadable video. Same guard exists in the downloader; if the Go rewrite drops it, ghost tweets pass through to the video pipeline and fail later.
  - The XPath `Promoted`/`Ad` match is over the whole subtree — false-positive risk if a user tweets those words. No sentinel test.
  - Duration extraction is best-effort; a `None` duration still gets emitted and the caller sorts by "top 5 longest" downstream, so `None` videos rank last but aren't excluded.

### 6. Download flow — `/download_video`

- **PURPOSE**: Bypass yt-dlp rate limits by piggybacking on the already-authenticated browser session's CDN cookies.
- **BEHAVIOR** (`session.py:778-916`, `app.py:291-333`):
  - Authorization gate: `authenticated and driver` truthy, else raises `TwitterAuthError` → HTTP 401.
  - `driver.get(tweet_url)` with 20 s page-load timeout, 2 s wait for player init.
  - Post-navigation re-checks for `login`/`flow` in URL (`session.py:809-811`); logs out state → 401.
  - Three URL-extraction methods in order: (1) `video[src]` containing `video.twimg.com` (`session.py:816-822`); (2) regex `https://video\.twimg\.com/…` over `page_source`, MP4 preferred (`session.py:829-841`); (3) JS walk over `<video>` and nested `<source>` elements (`session.py:845-862`). First match wins.
  - Downloads with `requests.get(video_url, cookies=<all-browser-cookies>, headers={UserAgent from driver, Referer: https://x.com/, Origin: https://x.com}, stream=True, timeout=30)` (`session.py:874-885`), streams 8 KB chunks to `output_path`.
- **REMARKS**:
  - The `Referer`/`Origin` on `x.com` is load-bearing — the CDN rejects raw requests without them. Silent regression if a Go port omits either.
  - Cookies are forwarded en masse; no filtering to `x.com` domain here (unlike backup). Works because `requests` sends them to `video.twimg.com` and Twitter's CDN authenticates the same session token.
  - Method 2's page-source regex will happily pick up a poster-image thumb URL if no MP4 exists — the `mp4_urls` filter mitigates but doesn't eliminate this.

### 7. Instance identity + registration lifecycle

- **PURPOSE**: Let the scaler / worker pool discover which twitter containers are up and route work to them, without hardcoded topology.
- **BEHAVIOR**:
  - `INSTANCE_ID = TWITTER_INSTANCE_ID or socket.gethostname()` (`app.py:29`). `CONTAINER_NAME = $HOSTNAME` (`app.py:31`). URL = `http://{CONTAINER_NAME}:{SERVICE_PORT}` (`app.py:34-37`).
  - After `session.startup()` returns (blocking or not), `register_with_registry()` fires (`app.py:40-50`), calling `src.scaler.registry.registry.register(INSTANCE_ID, url)`. Failure is logged non-fatal.
  - Heartbeat thread (`app.py:53-62`) calls `registry.heartbeat(INSTANCE_ID)` every 10 seconds; exceptions swallowed silently.
  - Lifespan shutdown calls `registry.unregister(INSTANCE_ID)` (`app.py:85-89`) then `session.cleanup()`.
- **REMARKS**:
  - Registry is imported lazily inside each function so twitter doesn't hard-depend on the scaler module. If the import fails, twitter runs fine but is invisible to the scaler — a "healthy in isolation, silently orphaned in prod" failure mode.
  - Registration happens *only after* auth completes; a container stuck at manual-login never registers.
  - `INSTANCE_ID` falling back to `socket.gethostname()` collides with `CONTAINER_NAME` under Docker Compose scaling. The `profile_dir` disambiguation uses a hash of `$HOSTNAME`, so multiple instances get distinct profiles even under the same instance-ID fallback path — but the *registry* row for two instances with the same hostname would collide. Real deployments always set `TWITTER_INSTANCE_ID` per compose service.

### 8. Error taxonomy

- **PURPOSE** (inferred): Signal auth-vs-generic failures so the caller can decide "retry later" (503/401) vs "propagate" (500).
- **BEHAVIOR**:
  - `/search`: 503 on `TwitterAuthError` (with a structured `detail` dict); 500 with `detail=str(e)` on anything else (`app.py:203-216`).
  - `/download_video`: 401 on `TwitterAuthError` or not-authenticated; 500 with `{status: "error", error: ...}` for extraction failures or `RuntimeError`. Extraction failures inside `download_video_direct` return `{status:"error", error:...}` normally but `/download_video` wraps them in a `JSONResponse(status_code=500,...)` — so an extract-fail HTTP-wise looks identical to a browser crash.
  - `/health`: 200 healthy or 503 unhealthy.
  - Auth endpoints: 500 on any exception. `/authenticate` is broken (see §1).
- **REMARKS**:
  - "500 for everything else" is indistinct on purpose — the workflow layer catches broadly and reschedules — but it loses signal on transient (network flap) vs permanent (extraction impossible) failures. The Go rebuild has room for a real error taxonomy here.

### 9. VNC container vs headless container

- **PURPOSE**: Separate the login-only surface (needs a visible desktop) from the scraping fleet (no desktop needed, fewer resources, no port conflicts).
- **BEHAVIOR** (per `README.md:132-146`, `session.py:106-149`):
  - `twitter-vnc` runs Xvfb + x11vnc + websockify on `DISPLAY=:99`, exposing VNC on host port 3203 (prod) / 4203 (dev). API on `:8888` internal.
  - Scaled `twitter-N` containers run headless — Selenium `--headless` flag, no Xvfb, no VNC.
  - Both mount the same host directory holding `twitter_cookies.json`. VNC container is where a human logs in; every scaled headless container restores from the shared JSON on next startup.
  - Manual Firefox in VNC container spawns with `DISPLAY=:99` explicitly (`session.py:320`) so the user sees it through noVNC.
  - Selenium headless mode is chosen by `TwitterConfig.headless` env-var (`config.py:20`) — same code path, `_setup_browser` reads the flag (`session.py:112-114`).
- **REMARKS**:
  - Both container roles share the *same singleton* `TwitterSessionManager` code — behavior identical except for the display. A rebuild could split them but doesn't need to.
  - Cookie flow is unidirectional: VNC container writes → shared file → headless containers read on next auth cycle. If a headless container's session goes stale mid-day, it hits `TwitterAuthError` and cannot self-heal until a human touches the VNC container. Silent operational gotcha.

### 10. Nuances a rebuild would silently regress on

- **60 s warm-path fast-check**: `ensure_authenticated` skips a full x.com/home GET if `last_activity` is <60 s ago. Saves ~3-4 s per search — critical during a goal burst where multiple queries fire back-to-back.
- **`busy` flag** (`session.py:70,509,515`): drives the scaler's scale-down safety; True only during `_do_search`. Read via `/status` (`app.py:158-170`). Rebuild must preserve this semantic exactly or the scaler will terminate mid-search.
- **`auth_token` presence guard** on both cookie backup (`session.py:293-296`) and restore (`session.py:216-218`): silently drops the operation. Load-bearing safety valve.
- **Instance-scoped profile dir** via hostname hash (`session.py:76-84`): parallel instances would corrupt each other's Firefox profile without this.
- **Idle-CPU containment Firefox prefs** (`session.py:139-141`): disables autoplay, GIF animation, backgrounded video decode. Without these, the warm-idle x.com tab bleeds ~20 % CPU per container.
- **Truncated snowflake rejection** (`session.py:632-638`, `scrape.py:75-86`): skips <18-digit tweet IDs before enqueue, avoiding downstream download failures.
- **`exclude_urls` → tweet-ID set** normalization (`session.py:526-530`): accepts both `/user/status/` and `/i/status/` shapes. Rebuild must keep both — callers pass a mix.
- **One-shot notification file** `/data/.login_notification_sent` (`session.py:98-104`, `399-404`): prevents email spam and clears itself once auth succeeds.
- **Cookie-backup file overwritten every successful search**: no consistency-of-content check. If a cosmic-ray flip corrupted `auth_token` mid-session, backup rewrites the broken cookies.
- **`_do_search` swallows per-tweet exceptions** silently (`session.py:669-670`): under-tested but intentional. Rebuild that surfaces these will need a rate-limited log.
- **Dead `/authenticate` endpoint** (`app.py:219-241` → nonexistent method): can be dropped in the rewrite.
- **`search_timeout=5`** in `config.py:31` is a *post-load sleep*, not a network timeout; renaming it in the rewrite would be less confusing.

---

## Upload Workflow — Python behavior spec (WHAT + WHY)

Files referenced: `archive/src/workflows/upload_workflow.py`, `archive/src/activities/upload/{core.py,dedup.py}`.

### 1. Signal-based FIFO pattern

**PURPOSE:** Serialize all S3+MongoDB writes for a single event through one Temporal workflow so concurrent DownloadWorkflows (DLWFs) cannot race each other on the shared dedup state.

**BEHAVIOR:**
- Per-event workflow ID `upload-{event_id}` (core.py:110); different events run in parallel.
- `signal-with-start` from `queue_videos_for_upload` (core.py:124–147): starts a new UploadWorkflow if none is running, otherwise delivers the signal to the existing instance.
- `add_videos` signal appends batches to a `deque` (upload_workflow.py:66–78); the main loop's `workflow.wait_condition` (upload_workflow.py:97–100) unblocks when the queue is non-empty or the idle timeout fires.
- Temporal guarantees signal delivery order → FIFO processing (first DLWF to signal = first to upload).
- `id_reuse_policy=ALLOW_DUPLICATE` (core.py:143) lets a late signal arriving after a Completed workflow start a fresh instance rather than silently drop; documented as the fix for the Lazio–Pisa stuck-event symptom.
- Empty-batch signals suppressed at the activity (core.py:93–102): telemetry recorded, signal skipped.

**REMARKS:** The workflow *is* the mutex — chosen so concurrency safety is Temporal-native, no DB row lock required. Fresh S3 state is fetched *inside* the serialized section (upload_workflow.py:197–201). The Go rewrite replacing idle-timeout with drain completion MUST preserve `ALLOW_DUPLICATE` for late-arriving signals.

### 2. Per-batch processing flow

**PURPOSE:** One `_process_batch` invocation fully absorbs one DLWF's videos into the shared S3+MongoDB state before the next batch is popped.

**BEHAVIOR:** In fixed order per `_process_batch` (upload_workflow.py:168–679):
1. Fetch fresh S3 state via `fetch_event_data` (line 206).
2. MD5 dedup — batch-internal + against S3 by filename hash (line 251).
3. Popularity bump for MD5 S3-exact matches (line 260).
4. Split MD5 replacements from perceptual candidates (lines 310–319).
5. Perceptual dedup, scoped verified↔verified and unverified↔unverified in parallel (line 343).
6. Popularity bumps for "existing was better" cases (line 412).
7. S3 uploads in parallel — new + replacements (line 469).
8. `save_video_objects` for new URLs (line 564).
9. `update_video_in_place` for successful replacements (line 595).
10. `recalculate_video_ranks` (line 623).
11. Frontend notify iff `videos_changed > 0` (line 640).
12. `_cleanup_uploaded_files` — individual files only, NOT the temp dir (line 659).

**REMARKS:** Fetching S3 state per-batch (not once per workflow) is what makes dedup race-free.

### 3. Verified vs unverified pools

**PURPOSE:** Prevent a verified goal clip from being replaced by an unverified clip of a *different* match moment that happens to look visually similar because it came from the same broadcast.

**BEHAVIOR:**
- After MD5 dedup, videos are partitioned by `timestamp_verified` (upload_workflow.py:327–328).
- Existing S3 videos partitioned the same way (lines 330–331).
- Two independent `deduplicate_videos` activities run concurrently via `asyncio.gather` (lines 343–358); verified pool sees only verified S3, unverified pool only unverified S3.
- If EITHER pool raises, both are zeroed and unverified `skipped_urls` is populated (lines 359–374) — the whole batch is skipped rather than uploaded-as-new.
- Results merged post-dedup (lines 377–393).

**REMARKS:** Same broadcast produces similar perceptual hashes across clock moments. A naive rewrite that folds both pools into one dedup pass regresses the exact bug the partition exists to prevent. The Go rewrite must keep the split.

### 4. Duration-vs-resolution winner picking

**PURPOSE:** Within a perceptual-duplicate cluster, disambiguate "same clip, trimmed differently" (prefer higher resolution) from "different clip of the same goal" (prefer longer).

**BEHAVIOR:**
- `DURATION_SIMILARITY_THRESHOLD = 0.15` (dedup.py:43).
- `_pick_best_video_from_cluster` (dedup.py:479–521): compute the longest as reference; for every cluster member compute `(max_dur − v.dur) / max_dur`.
- If ALL members are within 15% of longest → "similar" → pick highest `file_size` (line 517).
- If ANY member exceeds 15% → "different" → pick longest, `file_size` tiebreaker (line 521).

**REMARKS:** Percentage, not absolute seconds, because absolute breaks at both ends: 10s vs 15s = 50% (different clips) but 60s vs 65s = 8% (same clip). The constant was orphaned during the Phase 3 split (dedup.py:39–43) and silently `NameError`d during Crystal Palace v Rayo before being restored 2026-05-27 — keep it a named constant in Go, don't inline `0.15`.

### 5. Should-replace-S3 decision

**PURPOSE:** Extend the same 15% rule to a new candidate vs an already-uploaded S3 asset for quality-driven in-place replacement.

**BEHAVIOR:**
- `_should_replace_s3_video` (dedup.py:523–572).
- Duration-diff-pct ≤ 15% → replace only if `new_file_size > existing_file_size` (lines 560–567).
- Duration-diff-pct > 15% → replace iff new is longer (lines 568–572).
- Zero-duration fallback: file-size comparison (lines 547–551).
- Returns `(bool, reason_str)`; the reason threads into the log line (upload_workflow uses it via activity output at dedup.py:437–453).

**REMARKS:** The reason string is load-bearing for post-hoc Loki debugging — an operator must be able to tell why a specific replacement fired. Preserve reason strings in the Go port.

### 6. Popularity accumulation

**PURPOSE:** Track how many independent scraper sources found "this clip" so ranking favours widely-witnessed goals over one-shot uploads.

**BEHAVIOR:**
- Starts at 1 per successful download (dedup.py:109–110, 322–325).
- MD5-identical cluster of size N → survivor gets popularity N (dedup.py:114–116).
- Perceptual cluster → survivor gets `sum(popularity)` across the cluster (dedup.py:329–330), preserving popularity that MD5 dedup already accumulated.
- S3 match, existing wins → `existing.popularity += incoming.popularity` (dedup.py:449; upload_workflow.py:263–280).
- S3 match, new wins (replacement) → `new.popularity = old.popularity + incoming.popularity` (dedup.py:436; MD5 path dedup.py:188).
- Persisted via `bump_video_popularity` (core.py:549–585) OR baked into the video_object for `save_video_objects` / `update_video_in_place`.

**REMARKS:** Popularity survives replacement — this is *why* the in-place pattern (§7) exists. A "delete-then-insert" rewrite would zero the counter unless popularity is carried into the replacement candidate BEFORE the S3 PUT, not after.

### 7. Replace + absorb sequence

**PURPOSE:** Upgrade an existing S3 asset in place while keeping shared URLs valid and absorbing the old popularity atomically.

**BEHAVIOR:**
- Dedup marks the winning candidate with `_old_s3_key` (dedup.py:441–442).
- `upload_single_video` reuses `existing_s3_key` if provided (core.py:322, 351–356) — the S3 PUT overwrites the same key, so the URL is stable.
- MongoDB path uses `update_video_in_place` (core.py:477–547): positional `$set` at `events.{event_idx}.{S3_VIDEOS}.{video_idx}` (core.py:526–530) — one atomic update, no remove-then-add.
- Popularity is baked into `new_video_object` BEFORE the atomic write (upload_workflow.py:544–546).
- Frontend refresh fires for both new videos and in-place updates (upload_workflow.py:640).

**REMARKS:** The remove-then-add alternative was explicitly rejected — comment at upload_workflow.py:583–585 documents "avoids race where video disappears between remove and add". SSE consumers holding shared links would see a broken URL during the gap. Go rewrite: same key + atomic single-row update on Postgres's `video_asset` table.

### 8. Rank recalculation

**PURPOSE:** After any change to an event's video array, recompute a stable ordering so the frontend's `rank` field reflects current popularity and quality.

**BEHAVIOR:**
- Fires after every batch that produced new videos OR successful replacements (upload_workflow.py:622–634).
- Wraps `store.recalculate_video_ranks` (core.py:620–640); the ranking function itself lives in `mongo_store`.
- Max 2 attempts, logged as non-critical on failure (upload_workflow.py:632–634).
- Runs BEFORE frontend notify (line 640) so SSE consumers see fresh ranks on refresh.

**REMARKS:** Non-critical failure leaves ranks stale but data correct — the next batch's recalc catches up. Preserve the "non-fatal, retry-lite" posture in Go — don't fail the batch on rank failure.

### 9. VAR / event-removal handling

**PURPOSE:** Bail out cleanly when an in-flight event has been removed from the fixture (typically because API-Football revoked a VAR-overturned goal).

**BEHAVIOR:**
- `fetch_event_data` returns `{"status": "error", "error": "event_not_found"}` when the event is missing from `fixtures_active` (core.py:201–204).
- `_process_batch` short-circuits with `{"terminated_early": True, "reason": "event_removed"}` (upload_workflow.py:220–232).
- `run()` detects the flag and returns from the workflow immediately with terminal totals (upload_workflow.py:131–142) — no queue drain, no idle wait.
- Any remaining queued batches are discarded (they'd re-fail the same fetch anyway).

**REMARKS:** Hard terminate, not batch skip. A signal that arrives after termination will `ALLOW_DUPLICATE`-start a fresh workflow (§1), which will immediately re-detect and re-terminate — idempotent by design. Go rewrite: model as a domain-level typed error (e.g. `EventRemovedError`), not a magic-string check.

### 10. The 5-minute idle-timeout waste

**PURPOSE:** Document the flaw the Go rewrite is fixing — this is not a feature to preserve.

**BEHAVIOR:**
- After the 10th DLWF's batch drains, `workflow.wait_condition` (upload_workflow.py:97–100) sits waiting for a signal that will never come.
- The workflow blocks for `UPLOAD_WORKFLOW_IDLE_TIMEOUT_MINUTES` (line 92) — 5 minutes of Temporal worker occupancy per event, per goal.
- Only on timeout does it run `_check_and_mark_download_complete` as a failsafe (line 108; body 714–744) and then complete (line 156).

**REMARKS:** On a 20-goal Champions League night this is 100 wasted worker-minutes. Go contract: `dlwf_count == 10 AND queue empty → complete immediately`. Preserve the `_check_and_mark_download_complete` *intent* — that all 10 DLWFs are registered before the workflow exits — as an idempotent invariant enforced by the completion contract, not as a "run once on the way out" side effect.

### 11. Failure isolation

**PURPOSE:** One failed activity must not kill the workflow or lose queued batches; but a dedup failure MUST skip the batch, not upload duplicates.

**BEHAVIOR:**
- Top-level `try/except Exception` around `_process_batch` (upload_workflow.py:120–154): batch skipped, counter incremented, workflow survives to the next signal.
- `fetch_event_data` failure defaults `existing_s3_videos` to `[]` (lines 238–241) — degrades to "treat everything as new" (permissive).
- MD5 dedup failure defaults to passing all videos through (lines 294–298) — permissive.
- Perceptual dedup failure writes empty upload/replace lists AND fills `skipped_urls` (lines 359–374) — restrictive, batch's videos are DROPPED.
- Individual S3 upload failure caught per-task (core.py:511–515); other uploads in the batch proceed.
- Mongo save/update failures logged, non-fatal (lines 576–579, 610–614).

**REMARKS:** Two load-bearing invariants:

1. **Dedup-failure-skips-batch** (upload_workflow.py:360–374). The inline comment names the incident: uploading as-new when dedup errors caused a live duplicate-upload bug. A "fall back to uploading everything" rewrite regresses that fix.
2. **Cleanup does NOT touch the temp dir** (upload_workflow.py:446–447, 656–659, 685). TwitterWorkflow owns the temp lifecycle across all 10 DLWF attempts. Deleting the temp dir here would rip files out from under a still-running DLWF that shares the directory. The Go rewrite must keep upload-side cleanup at file granularity; directory cleanup stays with whoever owns the DLWF fan-out lifecycle.

---

## Video download + perceptual hashing — Python behavior spec (WHAT + WHY)

Files referenced: `archive/src/activities/{download.py, hashing.py}`, `archive/src/utils/{dedup_match.py, config.py}`.

### 1. Metadata pre-filter chain

**PURPOSE.** Reject unsuitable clips using cheap metadata checks before spending bandwidth or CPU on download, hashing, or vision.

**BEHAVIOR.**
- Pre-download stage reads `mediaDetails[].original_info` from the syndication response (width/height only — no duration/framerate yet), `download.py:358-390`.
- Short-edge gate: `min(width, height) >= MIN_SHORT_EDGE = 600 px` when `SHORT_EDGE_FILTER_ENABLED = True`, `config.py:57-58`. Bar tuned to allow letterboxed 720p (1280×686), not just clean 720p.
- Aspect ratio gate: `width/height` must lie in `[MIN_ASPECT_RATIO=1.75, MAX_ASPECT_RATIO=1.82]`, `config.py:68-69`, straddling 16:9=1.7778 with encoder slop.
- Pre-download filter order is short-edge, then aspect — either failure returns `status="filtered"` without spending a byte, `download.py:371-390`.
- Post-download re-verifies the same properties from ffprobe in case syndication metadata disagreed with the actual file, `download.py:601-625`.
- Duration gate runs only post-download: `MIN_VIDEO_DURATION=3.0 s` (strict; exactly 3.00s fails), `MAX_VIDEO_DURATION=90.0 s`, `download.py:585-599`, `config.py:72-73`.
- Post-download order: duration → short-edge → aspect. Any failure deletes the file and returns `None`, `download.py:585-625`.
- No framerate check at any stage.

**REMARKS.** Config comments at `config.py:60-66` cite the source-of-truth for the aspect band: prod S3 distribution 2026-06-30, 81% in 1.78-1.79, widened to 1.75-1.82 to absorb padding (1280×722=1.7729, 1280×705=1.8156) without admitting 16:10 letterboxed broadcasts (~1.60-1.72) or cinema clips (≥1.85). Phone-TV recordings pass this filter and are removed by AI vision downstream — bounds intentionally lenient. The `duration <= MIN` (not `<`) at `download.py:585` is deliberate; the comment at `:584` calls it out. The two-stage filter (metadata + ffprobe) exists because syndication `original_info` is sometimes wrong or missing. Framerate is not gated because Twitter has already normalized clips through re-encode by the time we see them.

### 2. Download flow

**PURPOSE.** Fetch the highest-bitrate MP4 variant from Twitter's public CDN without hitting rate-limited authenticated APIs.

**BEHAVIOR.**
- Extract `tweet_id` from URL via `/status/(\d+)` regex, `download.py:230-242`.
- Reject truncated Snowflakes (< 18 digits) up front with `VideoMalformedURLError`, `failure_mode=truncated_snowflake`, `download.py:256-273`.
- Call `cdn.syndication.twimg.com/tweet-result?id=<id>&token=x` with a browser UA + Twitter Referer, 5 s timeout, no auth, `download.py:276-289`.
- Variant path preferred: `mediaDetails[].video_info.variants` (has bitrate); fallback: `video.variants`, `download.py:356-394`.
- Sort MP4-only variants by bitrate desc and pick head, `download.py:402-414`.
- CDN stream download, 60 s timeout, 8 KB chunks; cookies (`auth_token`, `ct0`, `twid`, `guest_id`) attached so `amplify_video` variants can succeed, `download.py:430-487`.
- Files < 1 KB are deleted and re-raise `RuntimeError` for Temporal retry, `download.py:520-525`.
- Errors are classified into typed exceptions: `VideoNotAvailableError` (404), `VideoGeoRestrictedError` (403), `TwitterRateLimitedError` (429), `VideoCDNTimeoutError` (timeout), `VideoDownloadError` (generic), `download.py:291-321, 454-483`.

**REMARKS.** The 5 s syndication timeout is deliberate — comment at `download.py:286-287`: "if syndication API doesn't respond quickly, it's probably going to fail. Longer timeouts just delay the inevitable retry." Cookies are module-cached (`_twitter_cookies_cache`, `download.py:34-81`); only cookie *presence* is logged, values redacted. Partial failure is surfaced as an exception raise; retry policy lives in the DownloadWorkflow config and Temporal handles backoff. The rebuild should keep the typed error taxonomy — it is what makes "was the fixture blocked by geo or by rate limit?" answerable in Grafana without regex-scraping messages.

### 3. Full-video MD5 hash

**PURPOSE.** Provide a byte-identical dedup key so exact-duplicate re-uploads collapse without ever running perceptual comparison.

**BEHAVIOR.**
- MD5 computed with 4 KB read chunks over the whole file, returned hex, `download.py:658-664`.
- Runs on every downloaded clip that survives basic filters, stored as `file_hash` on the returned dict, `download.py:628, 645`.
- Batch dedup by MD5: within one download batch, candidates that share `file_hash` are collapsed to a single upload; popularity (source-URL count) fans in — the surviving candidate carries the aggregate. This happens downstream in the upload activity, not in `download.py` itself.
- `file_hash` is one of two dedup axes; the other is the perceptual hash.

**REMARKS.** MD5 is a dedup key, not a security guarantee — collisions on real re-encoded video bytes are effectively zero at this scale. 4 KB read chunking is a Python-idiom holdover; Go can safely use 64 KB+ with no behavior change. Popularity fan-in on shared MD5 is load-bearing for ranking — "seen from 8 tweets" beats "seen from 1 tweet" downstream. Do not silently discard the count on collapse. The download activity itself does not consult existing S3 for MD5; that check happens further downstream via the S3-key convention.

### 4. S3 filename convention

**PURPOSE.** Encode the MD5 in the S3 object key so a single `HEAD` answers "already in the corpus?" without a separate index.

**BEHAVIOR.**
- Local temp filename during download is `{event_id}_{video_index}_01.mp4`, `download.py:427` — that name is not the S3 key.
- The S3 key composition (in the upload activity, extracted P3b per `dedup_match.py:1-14`) includes the MD5, letting one `head_object` per candidate hash answer existence.
- The bucket listing IS the index. No sidecar catalog.

**REMARKS.** Textbook content-addressable-storage-on-S3; the rebuild should keep this shape and consider strengthening from MD5 to SHA-256 while preserving the "filename encodes hash" property.

### 5. Perceptual hash generation

**PURPOSE.** Produce a per-frame fingerprint dense enough to catch the same goal captured with different start offsets or minor re-encodes.

**BEHAVIOR.**
- Algorithm: dHash on a 9×8, grayscale, histogram-equalized frame, `hashing.py:96-108`. 9×8=72 pixels, adjacent-pixel compare per row → 8×8=64 bits.
- Histogram equalization normalizes contrast/brightness before resize so color-graded uploads don't drift the hash, `hashing.py:97`; the comment at `:47-48` calls out "handles color grading differences."
- Sample every 0.25 s starting at `t=0.25`, stop at `duration - 0.3 s` to avoid EOF glitches, `hashing.py:64, 124`.
- One fresh `ffmpeg -ss <ts> -vframes 1 -f image2pipe -vcodec png` subprocess per frame, 10 s timeout each, `hashing.py:75-89`.
- Storage: `dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,...` with hex16 hashes, `hashing.py:49, 145`.
- Fallback: if the loop produces zero frames, try one frame at `t=1.0 s`, `hashing.py:135-141`.
- Heartbeats fire before every ffmpeg call so long clips don't fail Temporal timeout under contention, `hashing.py:67-70, 126-127`.
- Only invoked AFTER AI validation confirms soccer content, `download.py:559-560`, `hashing.py:153`.

**REMARKS.** The dense text format is Python-convenient (one `split`) but expensive to parse in Go: `strings.Split` allocates, every hex needs `strconv.ParseUint`, and the matcher is O(N²) offsets over the parsed structure. Keep the string form as the wire/DB representation for backward-compat with existing S3-corpus hashes, but parse once into an in-memory `[]struct{ts float32; hash uint64}` — an order of magnitude faster on the hot loop. The single-frame fallback at `hashing.py:135-141` is effectively dead code under `MIN_VIDEO_DURATION=3.0` (the loop always produces ≥10 frames); the rebuild can drop it. One ffmpeg-per-frame is another Python-era shape worth revisiting — a single ffmpeg with a select filter can stream all frames in one invocation and cut subprocess overhead ~100×.

### 6. Match algorithm (`_dense_hashes_match`)

**PURPOSE.** Decide whether two dense hashes are the same goal even when the two clips start at different broadcast offsets.

**BEHAVIOR.**
- Both hashes parsed into `{timestamp: hash_int}` maps, `dedup_match.py:132-142`.
- Reject if either side has fewer than `MIN_CONSECUTIVE_MATCHES=3` frames, `dedup_match.py:147-148`, `config.py:79`.
- Outer double-loop over all `(start_a, start_b)` pairs establishes candidate offset `offset = start_b - start_a`, `dedup_match.py:155-157`.
- For each offset, walk `ts_a` in order; expected `ts_b = ts_a + offset`; accept any actual B-timestamp within `tolerance = interval_a / 2 = 0.125 s`, `dedup_match.py:163-171`.
- Per-frame match: Hamming ≤ `MAX_HAMMING_DISTANCE=10` bits, `config.py:78`, `dedup_match.py:174-177`.
- On match, `consecutive++`; on miss, reset to 0, `dedup_match.py:179-185`.
- Return True as soon as `consecutive` reaches 3, `dedup_match.py:180-183`.
- Legacy 3-hash format falls back to "2 of 3 match, any order," `dedup_match.py:82-99`.

**REMARKS.** 3 consecutive matches at 0.25 s = 0.75 s of matching video, chosen to reject false positives from goals scored 60 s apart in the same match with similar celebration framing (comment at `config.py:79`). Offset-tolerance of `interval/2` is what makes the algorithm robust to two clips whose 0.25 s sample grids happen to be shifted by ~0.1 s; without it, the same broadcast frame at slightly-different sample offsets would appear un-matched. Worst-case complexity is O(|A|²·|B|²) via nested linear scans; for 120-frame clips that's ~200M inner iterations. Real clips short-circuit early; the rebuild should still index `frames_b` by rounded timestamp so the tolerance lookup is O(1).

### 7. Ordering — MD5, perceptual, S3 corpus

**PURPOSE.** Run the cheap exact-match check first; only spend perceptual compute where it could actually change the answer.

**BEHAVIOR.**
- MD5 is computed at download time, `download.py:628`, long before any perceptual work.
- Perceptual hash is generated AFTER AI vision validation succeeds; comment at `download.py:559`: "Does NOT generate perceptual hash here"; comment at `hashing.py:153`: "Called AFTER AI validation to avoid wasting compute on non-soccer videos."
- Within a batch: MD5 dedup collapses byte-identical candidates first; perceptual dedup runs on the survivors.
- Against the S3 corpus: MD5 → S3 HEAD via the key convention; if hit, skip perceptual entirely.
- Perceptual is invoked only between MD5-differing pairs.
- Empty perceptual hash on either side → treated as no-signal, pair NOT collapsed, `dedup_match.py:70-71`.

**REMARKS.** The AI-vision-before-perceptual-hash ordering matters for compute budget — vision drops a significant fraction of candidates, so perceptual runs on ~1 in 3–5 downloaded clips. Reverse the order and ffmpeg subprocess count grows 3–5×. The rebuild should preserve this exact ordering.

### 8. Failure modes

**PURPOSE.** Every stage degrades to a typed, retriable outcome — never silent data loss.

**BEHAVIOR.**
- Missing/empty perceptual hash: `_perceptual_hashes_match` short-circuits False, `dedup_match.py:70-71`.
- No frames extracted: activity returns `{"perceptual_hash": "", "error": "no_frames_extracted"}`, `hashing.py:194-197`.
- Per-frame ffmpeg failure: `extract_frame_hash_normalized` returns `""` on non-zero return or timeout, `hashing.py:91-92, 113-117`; that timestamp is skipped, generation continues.
- ffprobe failure: caught, logged, returns zeros; downstream duration=0 fails MIN gate and file is deleted, `download.py:715-720, 585`.
- File < 1 KB: deleted, `RuntimeError` raised for Temporal retry, `download.py:520-525`.
- Invalid hash format on emit: returned as error, upload skipped, `hashing.py:198-202`.
- Heartbeat before every ffmpeg subprocess — the fix for the "4 concurrent hash-gens all timing out" mode; comment at `hashing.py:67-70` marks this CRITICAL.
- Hash parse errors: `_hamming_distance` returns 64 (max) on `ValueError/TypeError`, `dedup_match.py:32-37`; `_dense_hashes_match` returns False on any Exception, `dedup_match.py:189-190`.

**REMARKS.** The "return False on any Exception" blanket at `dedup_match.py:189-190` is intentionally lenient — over-uploading is a lesser evil than crashing the upload workflow — but the rebuild should log the exception rather than swallow it silently.

### 9. Historical fixes visible in comments

**PURPOSE.** Comments preserve production incidents whose fixes aren't obvious from code shape alone.

**BEHAVIOR.**
- Paderborn-Wolfsburg post-mortem, 2026-05-25: an upstream tweet-URL bug produced 13/14/17-digit Snowflakes that only failed at the syndication 404 stage; fix at `download.py:245-273` adds `MIN_SNOWFLAKE_LEN=18` and raises `VideoMalformedURLError` with `failure_mode=truncated_snowflake` so Grafana sees the shape.
- Phase 3 (P3a, 2026-05-26) extracted vision + hashing into their own modules from `src/activities/download.py`; back-compat re-exports at `download.py:758-772` preserve the old import path.
- Phase 3 (P3b, 2026-05-26) split dedup helpers into `src/utils/dedup_match.py`, header at `dedup_match.py:1-14`.
- `HASH_VERSION="dense:0.25"` (`config.py:77`) is stored per-video in Mongo precisely so an algorithm swap (Phase 5 image-embeddings anticipated at `hashing.py:8-11`) can coexist with old hashes rather than force a corpus-wide re-hash.
- Aspect band (1.75-1.82) is a distribution-driven decision, computed against prod S3 on 2026-06-30, `config.py:60-66`.
- `MODULE = "download"` kept inside `hashing.py` (`hashing.py:15-16, 30-31`) so existing Grafana dashboards filtering `module="download"` keep working after the split — filename is documentation-of-organization, MODULE is documentation-of-identity.

**REMARKS.** Every comment-cited incident produced the pair (typed error, queryable Grafana field) — the Go rebuild should carry that discipline forward. Legacy hash formats (`hash1:hash2:hash3` and `duration:hash1:hash2:hash3`) still live in `_parse_perceptual_hash` at `dedup_match.py:220-230`; the rebuild can drop them if the migration plan re-hashes surviving legacy S3 objects, else keep the branch.

---

## Vision / AI validation — Python behavior spec (WHAT + WHY)

File referenced: `archive/src/activities/vision.py`.

### 1. `validate_video_is_soccer` as the single combined call

PURPOSE: One vision LLM invocation per frame decides "is this soccer, is this a phone-of-TV recording, and what does the clock show" together, so each frame is billed once for all three concerns (lines 551-927).

BEHAVIOR:
- A single `_call_vision_model` call per frame returns one JSON object satisfying all three questions (lines 623-636, 742, 759).
- The activity is the sole entry point for validation (`@activity.defn`, line 551), so downstream code sees one Temporal activity, not three.
- Image tokens are loaded once per frame decision; splitting into three calls would triple that cost against joi's throughput budget.
- Structured output is enforced via `response_format: json_object` (line 462), giving the parser (lines 643-714) a deterministic contract.
- Retries, semaphore admission (line 52), timeouts, and typed errors apply per frame — not per subquestion — halving the failure surface.
- Return payload merges all three concerns (lines 914-927): `is_valid`, `is_soccer`, `is_screen_recording`, `clock_verified`, `extracted_minute`, `timestamp_status`, `extracted_clocks`.

REMARKS: The Phase 5 note (lines 24-27) foreshadows splitting the internals — soccer/screen may migrate to embeddings while OCR stays on the chat VL model — but the "one round-trip per frame" external contract is what callers depend on. The rebuild's Phase O4 should preserve that shape even if internals split.

### 2. Dual checkpoint at 25% and 75% frame positions

PURPOSE: Two frames sampled a half-video apart give redundancy against transient bad frames — a graphics-only cut, a black transition, a crowd close-up without the clock visible.

BEHAVIOR:
- Frame timestamps are `duration * 0.25` and `duration * 0.75` (lines 717-718), straddling the midpoint at any length.
- Each frame is fed to the same prompt in a separate call (lines 742, 759) and parsed independently.
- Agreement on both `soccer` and `screen` yields a fast answer at confidence 0.90-0.95 (lines 809-812, 830-831).
- Disagreement on either dimension triggers a 50% tiebreaker with 2/3 majority voting (lines 774-798, 815-819, 833-836).
- If one of the two frames fails extraction, single-frame fallback runs at confidence 0.7 (lines 801-808, 826-829); if both fail, the activity raises `RuntimeError` (lines 723-727).
- Each check emits a Temporal heartbeat so long videos don't time out (lines 741, 755, 783).

REMARKS: Cost is 2 LLM calls per video in the common case, 3 with tiebreaker. Only 25%/75% frames feed timestamp validation (line 794: "Extracted but NOT used for timestamp validation") — the tiebreaker exists only to resolve soccer/screen disagreement. The rebuild must preserve this; adding the 50% clock to the verification pool would silently loosen acceptance.

### 3. The 5-field JSON output shape

PURPOSE: The model returns exactly `{soccer, screen, clock, added, stoppage_clock}` (line 626) so the parser has an enumerable schema with clear null semantics.

BEHAVIOR:
- `soccer`: boolean; true means "soccer broadcast content of any kind" per the rubric (line 628).
- `screen`: boolean; true means "phone filming a TV" (line 630). Bias-toward-false when uncertain.
- `clock`: string like `"34:12"` or `"90:00"`, or `null` if no primary timer visible (line 632).
- `added`: string like `"+4"`, or `null` if no added-time indicator (line 634).
- `stoppage_clock`: string like `"03:57"`, or `null` if no separate sub-timer (line 636).
- Parser reads JSON directly (lines 667-674); a regex text-fallback path (lines 678-713) recovers responses from models that ignored `json_object`.
- Null on any clock field means "not visible in this frame" — distinct from "visible but wrong" — and drives the "unverified" branch below.

REMARKS: The 5-field shape is the contract downstream expects. `parse_response` normalizes to `is_soccer`, `is_screen`, `raw_clock`, `raw_added`, `raw_stoppage_clock` (lines 645-646) preserving null through to `validate_timestamp`. The rebuild's typed struct should mirror this exactly.

### 4. `soccer` rubric today

PURPOSE: True for any soccer broadcast footage — live match, replay, celebration, VAR, stadium recording (line 628).

BEHAVIOR:
- In scope per prompt (line 628): "players on pitch, match action, goals, replays, celebrations, VAR footage, stadium recordings."
- Out of scope per prompt (line 628): "studio/podcast, press conference, ads, other sports, or just graphics with no match footage."
- Text-fallback also accepts "SKIP" as soccer-true (lines 682-684), inherited from an older prompt.
- Classification is per frame — a promo insert at one frame won't reject the video if the other frame is clean.

REMARKS: **The user has flagged this as too lenient for production.** Including "celebrations" and "stadium recordings" unconditionally is why non-broadcast content (fan-shot phone videos of trophy lifts, stadium exteriors, tunnel walks) still passes. The rebuild's `docs/design/proposals/video-dedup.md` rubric should tighten this: celebrations should require in-play context (players in kit on pitch, immediate goal aftermath), stadium recordings should require active match play visible. Splitting `soccer` into `soccer_broadcast` vs `soccer_adjacent` would let ranking keep celebrations without letting them count as passing broadcast content.

### 5. `screen` rubric today

PURPOSE: True when a physical camera is filming a TV set (line 630): moiré, bezel, glare, tilt, room visible.

BEHAVIOR:
- Positive cues per prompt (line 630): "moiré patterns, visible TV bezel, screen glare, tilted angle, visible room/furniture."
- Explicit false-positives to reject (line 630): "professional broadcasts, overlays, scoreboards, watermarks, letterbox bars."
- Default-false posture: "When in doubt, false" (line 630) — bias toward keeping.
- Text-fallback fires on keywords `MOIRE`, `BEZEL`, `TV FRAME` (line 693).
- 2/3 majority to REJECT (line 836): asymmetric — you need at least two votes for screen-true.

REMARKS: **This does NOT catch software screen recording** (OBS-style browser capture, capture-card DVR, in-browser player recording). Those clips lack moiré and bezel because the signal is captured digitally, but they're still re-uploads of someone else's stream. This is a known rebuild-time gap; the rebuild's tightened rubric needs a separate signal for it (streaming-service watermarks, browser chrome, DVR progress bars) or must rely on S3-corpus dedup as the sole defense.

### 6. Clock extraction — three fields, one truth

PURPOSE: Broadcasts show *two* clocks during stoppage — a frozen main clock at 45:00 or 90:00 plus a smaller counting-up sub-clock — and both are needed to reconstruct absolute match minute.

BEHAVIOR:
- `clock` captures the primary timer (line 632). Parsed by `parse_clock_field` (lines 198-244) which handles running "34:12", period indicators "2H 15:30" / "ET 04:04", and compact stoppage "45+2".
- `added` captures the "+N" indicator (line 634). Parsed by `parse_added_field` (lines 247-260) — currently informational, not summed into absolute minute.
- `stoppage_clock` captures the sub-timer minute component (line 636). Parsed by `parse_stoppage_clock_field` (lines 263-279).
- `compute_absolute_minute` (lines 282-300) sums `clock + stoppage_clock` when both present: "90:00" main + "02:17" sub → minute 92.
- Smart offset in `parse_clock_field` (lines 238-244) disambiguates "2H 15:30" as 60 (relative) vs "2H 67:00" as 67 (absolute) by numeric magnitude.
- Sentinels "NONE", "HT", "FT", "HALF TIME", "FULL TIME" map to `None` (line 212).

REMARKS: `added` is captured but only `stoppage_clock` is summed — `added` is *allocated* stoppage time (bounds), `stoppage_clock` is *elapsed*. The rebuild should preserve both fields even though the current summation ignores `added`; it becomes load-bearing when OCR quality improves and we can trust "+N" as a range check.

### 7. Timestamp validation

PURPOSE: Compare each frame's extracted minute to the event's API-reported minute + extra, with ±1 tolerance, and classify verified / unverified / rejected (lines 303-369).

BEHAVIOR:
- Expected minute = `api_elapsed + (api_extra or 0) - 1` (line 337); the `-1` accounts for API reporting the minute *after* the goal.
- Direct match: any parsed frame minute within ±1 → `"verified"` (lines 351-354).
- OCR-correction phase: if the model dropped a leading digit ("92:36" read as "02:36"), rebase by adding `api_elapsed` and re-check ±1 (lines 361-365).
- No clock visible in any frame → `(False, None, "unverified")` (lines 348-349).
- No `api_elapsed` supplied (e.g., in-flight replay with default=0) → `(False, None, "unverified")` (lines 333-334).
- Clock visible in ≥1 frame but no phase matches → `"rejected"` with the closest minute returned for logging (lines 367-369).
- Only 25%/75% frames are fed in (lines 844-849); the 50% tiebreaker is deliberately excluded (line 794).

REMARKS: The three-state classification is the load-bearing output of this function — see §8.

### 8. `is_valid` derivation — REJECTED vs UNVERIFIED

PURPOSE: `is_valid = is_soccer AND NOT is_screen_recording AND timestamp_status != "rejected"` (lines 841-864).

BEHAVIOR:
- Baseline: `is_valid = is_soccer and not is_screen_recording` (line 842).
- If `timestamp_status == "rejected"`: `is_valid` is forced False (lines 862-863) — the video is **discarded**.
- If `timestamp_status == "unverified"` (no legible clock): `is_valid` stays True — the video is **kept** in the corpus.
- If `timestamp_status == "verified"`: `is_valid` stays True and downstream ranking gets a positive `clock_verified=True` signal.
- All three fields are returned in the payload (lines 923-925) so callers can rank kept-and-verified above kept-but-unverified.

REMARKS: **This is the load-bearing distinction the rebuild must preserve.** "Rejected" means the clock said the wrong minute — evidence of the wrong goal or wrong match — and discarding is safe. "Unverified" means no clock was legible — the clip might be a valid celebration or replay whose visible clock got covered by an overlay — and keeping it at lower rank protects recall. Collapsing these into a single "not verified → drop" would silently gut the corpus of legitimate no-clock footage. The video-dedup proposal's tightened rubric should keep this three-state output as the timestamp contract.

### 9. Handling low-confidence responses, JSON parse failures, LLM timeouts

PURPOSE: The LLM path is unreliable in three flavors — HTTP failure, parse failure, and semantic uncertainty — each with a distinct behavior.

BEHAVIOR:
- Semaphore-gated concurrency: `_LLM_SEMAPHORE = asyncio.Semaphore(LLM_CONCURRENCY_PER_WORKER)` (line 52), pinned to 2 per worker to match joi's `--parallel 4` / 2-worker configuration.
- Retries up to 3 times on `httpx.TimeoutException` and `httpx.ReadError` with linear backoff (`3 * attempt` seconds, lines 511-517).
- 503 from joi is logged distinctly as `vision_cap_exceeded` (lines 482-487) so parallel-cap contention is diagnosable.
- Connect failures raise typed `LLMUnavailableError` (lines 501-508); exhausted retries raise `LLMTimeoutError` (lines 518-529); unexpected exceptions raise `LLMValidationError` (lines 530-541).
- Non-200 non-503 responses log `vision_http_error` and return None after retries (lines 488-500).
- Parse failure path: `parse_response` catches `json.JSONDecodeError` and `TypeError` (line 675), falls back to regex text parsing (lines 678-713); if that finds nothing, returns all-false / all-null.
- No confidence field from the model — confidence in the return payload (lines 811, 818, 822) is derived from voting agreement, not the LLM.
- Video too short (<1s duration) short-circuits to `is_valid=True, confidence=0.5` (lines 611-619).

REMARKS: Graceful degradation is deliberate — LLM outages should not force-drop videos. But it also means a hallucinating model would happily pass everything at confidence 0.9. The rebuild should consider an explicit "LLM produced no usable signal" state distinct from "LLM said not-soccer."

### 10. Frame extraction subroutine

PURPOSE: `_extract_frame_for_vision` pulls a single PNG frame at a target timestamp via ffmpeg and returns it base64-encoded for the LLM's `image_url` field (lines 377-422).

BEHAVIOR:
- Command: `ffmpeg -ss <ts> -i <file> -vframes 1 -f image2pipe -vcodec png -` piped to stdout (lines 392-400) — no on-disk intermediate.
- 10-second subprocess timeout (line 406) with distinct log actions for `frame_extraction_failed`, `frame_extraction_timeout`, `frame_extraction_error` (lines 409, 416, 420).
- Duration is probed once via `ffprobe format=duration` before frame extraction (lines 598-605); failure defaults to 10.0s (line 609).
- 25%/75% timestamps are always `duration * 0.25` / `duration * 0.75` — no length-specific branch beyond the <1s bail (line 611).
- If EITHER frame extracts, validation proceeds (lines 723-727); only if BOTH fail does the activity raise `RuntimeError`.
- Returned base64 is embedded as `data:image/jpeg;base64,...` despite being PNG (line 453) — llama.cpp tolerates the content-type mismatch.

REMARKS: The <1s bail is the only short-video branch — a 2s clip still gets 25%/75% sampling (frames at 0.5s and 1.5s). The rebuild should consider an explicit minimum-spacing rule so both frames aren't effectively the same shot.

### 11. Historical notes visible in the file

PURPOSE: The docstring and inline comments carry three forward-looking pieces of context.

BEHAVIOR:
- **Phase 3 module split** (P3a, 2026-05-26) — vision was extracted from `download.py`; `MODULE = "download"` (line 48) is deliberately kept so Grafana dashboards and the Phase 1 query catalog keep working (lines 14-18). Log-identity vs code-organization are documented as intentionally separate.
- **Phase 5 planned replacement** (lines 24-27) — soccer/screen classification may migrate to Qwen3-VL embedding-based classification, leaving only OCR (clock/added/stoppage_clock) on the chat VL model. The clock parsers survive that migration.
- **Legacy `parse_broadcast_clock`** (lines 60-190) is retained only for the `test_clock_parsing.py` harness (lines 96-99); production uses the structured field parsers.
- **Text-fallback parsing** (lines 678-713) is a backstop from before `response_format: json_object` was reliable; kept for older models.
- **`LLM_CONCURRENCY_PER_WORKER = 2`** (line 52) is pinned to joi's `--parallel 4` / 2-worker config; changing joi's concurrency requires a paired change here.

REMARKS: Phase 5's soccer/screen → embeddings direction aligns with the rebuild's video-dedup proposal — keep OCR on the chat model, move classification to embeddings. The `MODULE = "download"` continuity discipline should be re-decided in the rebuild's `vocabulary` registry rather than silently inherited; it is a Python-era log-schema constraint that Grafana queries lock in place, and the Go rewrite has an opportunity to name the module correctly at the source.

---

## Scaler / Registry + Consumer surface — Python behavior spec (WHAT + WHY)

Files referenced: `archive/src/scaler/{registry.py, scaler_service.py}`; `archive/api/`; `archive/deploy/INFRA-NOTES.md`.

### SECTION A — Scaler + Twitter Instance Registry

### A1. TwitterRegistry singleton (Mongo-backed)

**PURPOSE**: Give Twitter scraper containers a way to advertise "I exist and I'm healthy" into a shared Mongo collection so other services can discover them dynamically.

**BEHAVIOR**
- `TwitterRegistry` is a thread-locked singleton (`registry.py:29-39`); one process gets one instance.
- Backing store is `db.twitter_instances` in MongoDB, lazy-loaded via `_get_store()` (`registry.py:55-60`).
- `register(instance_id, url)` upserts `{instance_id, url, status:"available", last_heartbeat, registered_at}` (`registry.py:62-86`).
- `heartbeat(instance_id)` bumps `last_heartbeat` only; sent every 10 s from a daemon thread inside each Twitter container (`twitter/app.py:53-62`).
- `unregister(instance_id)` flips `status="unavailable"` on shutdown; the row is not deleted (`registry.py:88-100`).

**REMARKS**: Instance identity is `TWITTER_INSTANCE_ID` env, falling back to `socket.gethostname()`. The URL each instance publishes is `http://$HOSTNAME:$TWITTER_SERVICE_PORT` — container name, port 8888. Perfect fit for a pg-backed row-per-instance table in the rebuild.

### A2. Load-balancing strategies exposed

**PURPOSE**: Let callers pick one healthy Twitter URL per request.

**BEHAVIOR**
- `get_instance_url(strategy)` supports `round_robin` (default), `random`, `first` (`registry.py:155-181`).
- Round-robin increments `_round_robin_index` under the class lock — safe across threads within one process, but each process has its own counter (no global fairness across worker containers).
- Single-instance short-circuit at `registry.py:170` returns the sole URL without consulting `strategy`.
- Public convenience wrapper `get_twitter_url()` calls the singleton with the default strategy (`registry.py:232-234`).

**REMARKS**: Round-robin was chosen because Twitter searches are near-uniform in cost and a random distribution was measurably lumpier under low instance counts. Preserve the "single-instance short-circuit" — it's the dev-mode ergonomics knob.

### A3. Staleness cutoff (30 s default)

**PURPOSE**: Automatically remove a Twitter instance from the routing pool if it stopped heart-beating.

**BEHAVIOR**
- `get_available_instances(max_age_seconds=30)` filters on `status="available" AND last_heartbeat >= now - 30s` (`registry.py:115-153`).
- Stale rows are never returned but are NOT deleted or status-changed by the reader — they just fall out of the filter until they heartbeat again.
- On any Mongo error the last cached list is returned; if none exists, the singleton falls back to `TWITTER_SESSION_URL` (default `http://found-footy-prod-twitter:8888`).
- Heartbeat cadence is 10 s, so the cutoff gives ~3 missed heartbeats before eviction.

**REMARKS**: The rebuild should preserve the "eviction is passive" property — writing to a stale row on read would create contention.

### A4. mark_instance_busy / mark_instance_available — an UNUSED hook

**PURPOSE**: Nominally, let callers signal a Twitter instance is currently occupied so routing skips it.

**BEHAVIOR**
- `mark_instance_busy(instance_id)` and `mark_instance_available(instance_id)` flip the `status` field between `"busy"` and `"available"` (`registry.py:183-203`).
- **Nothing in the archive calls these two methods.** Grep across `archive/` returns zero hits outside the definitions themselves.
- Worse: `get_available_instances` filters on `status="available"`, so if anything ever DID call `mark_instance_busy`, the routing query would immediately drop that URL until the same caller flipped it back. There is no watchdog to reset a stuck "busy" row — a crash mid-search would strand an instance out of rotation.
- The routing query the actual worker uses (`src/activities/twitter.py:283-344`) doesn't call the registry AT ALL — it independently probes `http://found-footy-prod-twitter-{1..8}:8888/health` on a 30 s cache and round-robins the healthy set. So the registry is *populated* but *not consulted* by the search path.

**REMARKS**: This is a real bug / dead code. The rebuild's pg-backed instance registry should either wire routing through it end-to-end or drop the busy/available API and stick to heartbeat-only liveness.

### A5. Scaler service — separate binary, docker-compose driver

**PURPOSE**: Watch load signals and scale worker + twitter container counts up/down.

**BEHAVIOR**
- Separate process, entry point `python -m src.scaler.scaler_service` (`scaler_service.py:570-571`), packaged as its own prod-only container.
- Loop cadence: 30 s (`CHECK_INTERVAL`, `scaler_service.py:50`). 60 s cooldown between actions (`SCALE_COOLDOWN`).
- **Worker signal**: count of RUNNING Temporal workflows via `ListWorkflowExecutions` (`scaler_service.py:107-138`). Scale up if `active_workflows/current_workers > 5`, scale down if `< 2` (`calculate_target_workers`, `scaler_service.py:325-351`). Min 2, max 8.
- **Twitter signal**: Mongo aggregation counting events with `_monitor_complete=true AND _download_complete not true` across `fixtures_active` + `fixtures_live` (`get_active_twitter_goals`, `scaler_service.py:148-179`). Each instance handles `TWITTER_GOALS_PER_INSTANCE=2` goals. Scale up if goals/instance > 2; scale down if `active_goals < instances`. Min 2, max 8.
- Scaling method: `python_on_whales` DockerClient invokes `docker compose up --scale <service>=<n>` in-process (`scaler_service.py:280-311`); idempotent — no-op if already at target.
- Emits state-change or heartbeat log every 30 s with a full metric snapshot including `total_goals`, `todays_goals`, `total_videos` (from a second Mongo aggregation, `get_goals_summary`).

**REMARKS**: The scaler owns compose-side scaling, not the workers themselves. Rebuild target is to move signals off Mongo aggregations onto pg queries and preserve the "two independent load signals" split — workers scale on active workflow count, twitter scales on active goal count. Don't merge them.

### A6. Fallback URL when no instances

**PURPOSE**: Keep the worker from crashing during a cold-boot window.

**BEHAVIOR**
- If `get_available_instances()` returns empty (no rows or all stale), `get_instance_url()` logs a warning and returns `TWITTER_SESSION_URL` (`registry.py:166-168`).
- Env var defaults to `http://found-footy-prod-twitter:8888` — a legacy singular container name that doesn't exist in prod anymore (`registry.py:53`).
- A second, separate fallback lives in `get_healthy_twitter_urls` (`scaler_service.py:516-552`): if no `twitter-{1..8}` responds to `/health`, returns the first two URLs blindly.
- The worker's Twitter activity has a third, independent fallback: probes `twitter-{1..8}` and falls back to the first two if none respond (`activities/twitter.py:322-328`).

**REMARKS**: Three independent fallback paths, one dead default hostname. The rebuild should have exactly one.

### A7. Cache TTL (5 s local cache)

**PURPOSE**: Keep the Mongo query off the hot path when many searches fire in the same second.

**BEHAVIOR**
- `_local_cache` + `_cache_time` in the singleton; `_cache_ttl=5s` (`registry.py:47-50`).
- Refresh path: on cache miss (age > 5 s OR cache empty), query Mongo and repopulate.
- Logs `instance_cache_refreshed` only when the returned URL count differs from the cached count — quiet during steady state, noisy during scale events.
- On Mongo error the cache is returned even if stale; the singleton NEVER re-raises to callers.

**REMARKS**: The parallel cache in the worker's own routing path (`activities/twitter.py:288`, 30 s TTL) exists because the registry is not consulted. Rebuild collapses these into one.

---

### SECTION B — Consumer Surface

**Overall shape**: The archive contains a FastAPI app at `archive/api/` mounted at `/api/v1`, but it is a *migration-in-progress parallel surface*, not the primary consumer contract. The primary contract the vedanta-systems frontend actually hits today is the Express BFF inside the vedanta-systems repo (not in this tree). The worker dual-publishes to both.

### B8. Surfaces that exist

**PURPOSE**: Give the frontend fixture/event data and a real-time invalidation signal.

**BEHAVIOR**
- FastAPI app in `archive/api/app.py`, `uvicorn api.app:app --host 0.0.0.0 --port 8080` (`app.py:7`).
- Routers under `/api/v1`: `health`, `fixtures`, `events`, `search`, `stream`, `internal` (`app.py:56-63`).
- REST reads: `GET /dates`, `GET /fixtures?date=YYYY-MM-DD`, `GET /fixtures/{id}`, `GET /events/{event_id}`, `GET /search?q=` (`routers/fixtures.py`, `events.py`, `search.py`).
- SSE at `GET /stream` (`routers/stream.py`).
- Webhook-in: `POST /internal/notify` (worker → API broadcast trigger, `routers/internal.py`).
- No webhook-out.

### B9. Auth pattern

**BEHAVIOR**
- Public REST + SSE: **unauthenticated**. CORS defaults to `*` (`settings.py:28-30`) with a "prod should set this explicitly" comment.
- `/api/v1/internal/*` gated by an `X-Internal-Token` header matching `INTERNAL_TOKEN` env (`deps.py:41-52`). Empty token = no-op auth, relying on docker-network isolation (`internal.py:6-11`).
- No session cookies, no bearer tokens on the read paths, no IP allowlist.

### B10. Data shapes

**BEHAVIOR**
- `/fixtures` returns three flat arrays: `staging`, `active`, `completed` — projected via `_FIXTURE_PROJECTION` (`fixtures.py:28-61`), covering fixture identity, league, teams, goals, and per-event fields `_monitor_complete`, `_download_complete`, `_s3_urls`, `_s3_videos`, `_first_seen`, `_telemetry`.
- Video URLs are rewritten at the boundary: stored `/video/<bucket>/<key>` becomes public `/api/v1/videos/<bucket>/<key>` (`fixtures.py:64-70`) — decouples clients from the storage layout. The actual video-serving handler is not in the archive, so this rewrite currently points at a not-yet-implemented endpoint.
- `/events/{id}` returns `{event_id, found, date, fixture_id, collection, status}` where `status` is derived server-side from the raw flags ("watching" | "extracting" | "complete" | "validating", `events.py:26-57`).
- `/search` regex-matches on team + player + assist names across all three collections, groups by date, tags each fixture with `_search.{team_match, matched_event_ids, match_count}`.

### B11. Where the vedanta-systems frontend actually hits

**BEHAVIOR**
- Not this FastAPI directly. Per `deploy/INFRA-NOTES.md:9`: "found-footy has no public-facing component (no Cloudflare tunnel ingress)".
- The frontend hits the vedanta-systems Express BFF (`vedanta-systems-prod-api:3001` per `deploy/INFRA-NOTES.md:61`), which proxies to found-footy's Mongo directly today.
- The FastAPI is a same-tailnet-only service reachable at `found-footy-{env}-api.{$BASE_DOMAIN}` via Caddy (compose-file comment at `docker-compose.dev.yml:210`), staged for a future cutover.

### B12. Caddy / reverse-proxy setup

**BEHAVIOR**
- Reference Caddyfile block in `archive/deploy/INFRA-NOTES.md:24-37` — routes only Temporal-UI, Mongo-Express, MinIO, and Twitter-VNC. **No route to the FastAPI is in the archive's INFRA-NOTES** despite the compose comment claiming one should exist.
- `X-Accel-Buffering: no` header set on the SSE response (`stream.py:57`) — the load-bearing hint to Caddy not to buffer text/event-stream.
- CORS `expose_headers` includes `Content-Range` / `Accept-Ranges` / `Content-Length` (`app.py:50`) — signals the eventual video-serving endpoint will support HTTP range requests.

### B13. Real-time vs polling

**PURPOSE**: Push a "something changed, refetch" signal so the frontend doesn't poll.

**BEHAVIOR**
- SSE, not polling. `EventSourceResponse` from `sse-starlette` with typed envelopes (`stream.py`).
- Envelope schema (`envelope.py:19-38`): `{type, id, ts, data}`. Types: `connected` (once per open), `invalidate` (data changed), `heartbeat` (idle keepalive), `health`.
- Trigger path: worker's `notify_frontend_refresh` activity (`activities/monitor.py:765-849`) is called from every workflow that mutates fixture/event state (ingest, monitor, twitter, upload workflows — 5 sites) and **dual-publishes** to (a) legacy vedanta-systems Express `POST /api/found-footy/refresh` (coarse `{type:'refresh'}`) and (b) FastAPI `POST /api/v1/internal/notify` (typed envelope with entity + ids + fields).
- SSE broadcast is bounded-queue per connection (`maxsize=100`, `sse.py:60`). Slow-client policy: on `QueueFull`, drop the connection — EventSource reconnects and catches up via REST (`sse.py:75-83`).
- Per-connection heartbeats every 30 s of idle (`SSE_HEARTBEAT_INTERVAL_S`, `sse.py:99-112`).
- sse-starlette `ping=15` sends TCP-keepalive comment lines separate from envelope heartbeats (`stream.py:53`).
- Monotonic `id:` field on every event supports `Last-Event-ID` in principle but is not honored on reconnect — replay is via REST (`stream.py:12-15`).

**REMARKS**: The dual-publish is load-bearing for the rebuild. Until the FastAPI is the frontend's direct target, the Go rewrite's SSE bridge must ALSO hit the legacy Express endpoint (or the vedanta-systems Express must be decommissioned/migrated first). Preserve the envelope schema exactly — it's what the copy-paste target project referenced in `envelope.py:2-3` is designed against.

### Preserve exactly in the rebuild

- SSE envelope shape (`type`, `id`, `ts`, `data`) and the four constructor types (`connected`, `invalidate`, `heartbeat`, `health`). Copy-paste contract.
- URL prefix `/api/v1/*` including `redirect_slashes=False`.
- REST projections + video-URL rewrite (`/video/...` → `/api/v1/videos/...`).
- Server-side status derivation for events.
- Dual-publish for the invalidation trigger during migration; single-publish only after vedanta-systems Express is retired.
- Twitter scaling on active-goal count in Mongo/pg, not Temporal queue depth.
- The 30 s heartbeat cutoff for Twitter instances.

### Build from scratch / drop

- `mark_instance_busy` / `mark_instance_available` — cut them unless the routing path is redesigned to consult the registry. The current worker path bypasses the registry entirely.
- Three redundant fallback URL paths — collapse to one.
- The stale `TWITTER_SESSION_URL` default (`http://found-footy-prod-twitter:8888`) — no such container exists.
- `python_on_whales`-driven `docker compose --scale`. The rebuild's scaler should target whatever orchestration primitive replaces this.
- CORS `*` default — set explicit origin.
- Round-robin fairness across worker processes — either accept "per-process fair" or move the counter into the registry.
