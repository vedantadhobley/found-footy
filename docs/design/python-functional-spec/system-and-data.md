# Python system and data behavior

Frozen legacy behavior from the [Python functional-spec index](./README.md).

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
