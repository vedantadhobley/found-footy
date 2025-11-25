# ⚽ Found Footy - Automated Football Goal Highlights Pipeline

**End-to-end automation** - detects football goals in real-time, validates stability with hash-based debounce, discovers videos on Twitter, downloads and deduplicates, then stores in S3 with metadata tags. Built with Dagster orchestration and MongoDB storage.

---

## 🎯 Complete Pipeline Flow

```mermaid
graph TB
    subgraph "🗓️ Daily Ingestion (00:05 UTC)"
        A1[Ingestion Job]
        A2[Fetch Today's Fixtures<br/>from API-Football]
        A3{Categorize<br/>by Status}
        A4[fixtures_staging<br/>TBD, NS]
        A5[fixtures_active<br/>LIVE, 1H, HT, etc]
        A6[fixtures_completed<br/>FT, AET, PEN]
        
        A1 --> A2 --> A3
        A3 -->|Not Started| A4
        A3 -->|In Progress| A5
        A3 -->|Finished| A6
    end
    
    subgraph "⏱️ Monitor Job (Every Minute)"
        B1[Monitor Job]
        B2[Activate Ready Fixtures<br/>staging → active<br/>with empty events]
        B3[Batch Fetch Active Fixtures<br/>from API-Football]
        B4[Filter Events<br/>Goals only]
        B5[Generate Event IDs<br/>fixture_team_Goal_#]
        B6[Store in fixtures_live<br/>overwrite each poll]
        B7{Compare<br/>live vs active}
        B8[Complete Finished Fixtures<br/>active → completed]
        
        B1 --> B2 --> B3 --> B4 --> B5 --> B6 --> B7
        B7 --> B8
    end
    
    subgraph "🎯 Debounce Job (Per Fixture)"
        C1{Trigger<br/>Condition?}
        C2[Build Event Dict<br/>by _event_id]
        C3[Iterate Active Events<br/>pop from dict]
        C4{Event in<br/>live dict?}
        C5[Calculate Hash<br/>of event data]
        C6{Hash<br/>changed?}
        C7[Increment stable_count<br/>Add snapshot]
        C8[Reset stable_count=1<br/>Add snapshot]
        C9{stable_count<br/>>= 3?}
        C10[Mark debounce_complete<br/>Trigger Twitter Job]
        C11[Add NEW Event<br/>to active<br/>stable_count=1]
        C12[Mark REMOVED<br/>_removed=true<br/>VAR disallowed]
        
        B7 -->|NEW event<br/>OR incomplete<br/>OR removed| C1
        C1 -->|Yes| C2 --> C3 --> C4
        C4 -->|Yes| C5 --> C6
        C6 -->|No| C7 --> C9
        C6 -->|Yes| C8
        C9 -->|Yes| C10
        C9 -->|No| C3
        C4 -->|No, leftover| C11
        C3 -->|In active<br/>not in live| C12
    end
    
    subgraph "🐦 Twitter Job (Per Stable Event)"
        D1[Twitter Job]
        D2[Build Search Query<br/>team + player + minute]
        D3[POST to twitter-session<br/>:8888/search]
        D4[Parse Response<br/>Extract video URLs]
        D5[Save discovered_videos<br/>in active event]
        D6[Mark twitter_complete<br/>Store video_count]
        D7[Trigger Download Job]
        
        C10 --> D1 --> D2 --> D3 --> D4 --> D5 --> D6 --> D7
    end
    
    subgraph "⬇️ Download Job (Per Event with Videos)"
        E1[Download Job]
        E2[Fetch Event Data<br/>from fixtures_active]
        E3[Download Videos<br/>to /tmp with yt-dlp]
        E4[Calculate File Hashes<br/>MD5 per video]
        E5{Duplicate<br/>hash?}
        E6[Keep Largest File<br/>best quality]
        E7[Upload to S3<br/>fixture_id/event_id/]
        E8[Add Metadata Tags<br/>player, team, event_id]
        E9[Mark download_complete<br/>Store s3_urls + count]
        E10[Cleanup /tmp]
        
        D7 --> E1 --> E2 --> E3 --> E4 --> E5
        E5 -->|Yes| E6 --> E7
        E5 -->|No| E7
        E7 --> E8 --> E9 --> E10
    end
    
    subgraph "💾 Storage"
        F1[(fixtures_staging)]
        F2[(fixtures_live)]
        F3[(fixtures_active)]
        F4[(fixtures_completed)]
        F5[☁️ MinIO S3<br/>fixture_id/event_id/<br/>videos with tags]
        
        A4 -.-> F1
        B6 -.-> F2
        C7 -.-> F3
        C8 -.-> F3
        C11 -.-> F3
        D6 -.-> F3
        E9 -.-> F3
        B8 -.-> F4
        E7 -.-> F5
    end
    
    style A1 fill:#e1f5ff
    style B1 fill:#fff3e0
    style C1 fill:#f3e5f5
    style D1 fill:#e8f5e9
    style E1 fill:#fce4ec
```

---

## 📋 Job Breakdown

### 1️⃣ Ingestion Job (Daily, 00:05 UTC)
**Purpose:** Fetch today's fixtures and categorize by status

**Operations:**
- Fetch fixtures from API-Football for current date
- Route to correct collection based on status:
  - `TBD`, `NS` → `fixtures_staging` (not started)
  - `LIVE`, `1H`, `HT`, `2H`, `ET`, `P`, `BT` → `fixtures_active` (in progress)
  - `FT`, `AET`, `PEN` → `fixtures_completed` (finished)

---

### 2️⃣ Monitor Job (Every Minute)
**Purpose:** Track active fixtures and detect event changes

**Operations:**
1. **Activate:** Move ready fixtures from `staging` → `active` (empty events)
2. **Fetch:** Batch fetch active fixtures from API-Football
3. **Filter:** Keep only trackable events (Goals only)
4. **Generate IDs:** Create sequential event IDs per team+event_type
   - Format: `{fixture_id}_{team_id}_{event_type}_{#}`
   - Example: `1378993_40_Goal_1`, `1378993_40_Goal_2`
5. **Store:** Save in `fixtures_live` (overwrites each poll)
6. **Compare:** Check for 3 trigger conditions:
   - **NEW:** Event in live but NOT in active
   - **INCOMPLETE:** Event in both but `_debounce_complete=false`
   - **REMOVED:** Event in active but NOT in live (VAR)
7. **Trigger Debounce:** Invoke debounce job per fixture with changes
8. **Complete:** Move finished fixtures to `completed`

---

### 3️⃣ Debounce Job (Per Fixture)
**Purpose:** Validate event stability with hash-based tracking

**Operations:**
1. **Build Dict:** Create `{_event_id: event}` from `fixtures_live`
2. **Iterate Active:** Process each event in `fixtures_active`
3. **Calculate Hash:** MD5 hash of event data (player, team, time, detail)
4. **Compare Hash:**
   - **Unchanged:** Increment `stable_count`, add snapshot
   - **Changed:** Reset `stable_count=1`, add snapshot
5. **Check Stability:** If `stable_count >= 3`:
   - Mark `_debounce_complete=true`
   - Trigger Twitter job
6. **Process NEW:** Leftover events in dict = new, add to active
7. **Mark REMOVED:** Active events not in live = VAR disallowed

**Enhancement Fields Added:**
- `_event_id`, `_stable_count`, `_debounce_complete`
- `_first_seen`, `_snapshots[]`, `_score_before`, `_score_after`
- `_twitter_search`, `_twitter_complete`

---

### 4️⃣ Twitter Job (Per Stable Event)
**Purpose:** Discover goal videos on Twitter

**Operations:**
1. **Build Query:** Format search string
   - Pattern: `"{team_name}" "{player_name}" {minute}'`
   - Example: `"Liverpool" "D. Szoboszlai" 13'`
2. **Search:** POST to `twitter-session:8888/search`
3. **Parse Results:** Extract video URLs from tweets
4. **Save:** Store `_discovered_videos[]` in active event
5. **Mark Complete:** Set `_twitter_complete=true`, `_video_count`
6. **Trigger Download:** Invoke download job if videos found

**Saved Video Metadata:**
- `tweet_url`, `tweet_text`, `video_url`, `author_username`

---

### 5️⃣ Download Job (Per Event with Videos)
**Purpose:** Download, deduplicate, and upload videos to S3

**Operations:**
1. **Fetch Event:** Get event data from `fixtures_active`
   - Extract: `event_id`, `fixture_id`, `player_name`, `team_name`
2. **Download:** Use `yt-dlp` to download videos to `/tmp`
   - Format: Best quality, MP4
3. **Calculate Hashes:** MD5 hash per downloaded file
4. **Deduplicate:** If hash matches:
   - Keep largest file (best quality)
   - Delete duplicates
5. **Upload to S3:**
   - Key: `{fixture_id}/{event_id}/{filename}`
   - Example: `1378993/1378993_40_Goal_1/video_001.mp4`
6. **Add Metadata Tags:**
   - `player_name`, `team_name`, `event_id`, `fixture_id`
7. **Mark Complete:** Update event with:
   - `_download_complete=true`
   - `_s3_urls[]`, `_s3_count`
8. **Cleanup:** Remove `/tmp` directory

---

## 🗄️ 4-Collection Architecture

### Event Enhancement Pattern

**fixtures_live:** Raw API data with `_event_id` generated (filtered to Goals only):
```javascript
{
  "player": {"id": 234, "name": "D. Szoboszlai"},
  "team": {"id": 40, "name": "Liverpool"},
  "type": "Goal",
  "detail": "Normal Goal",
  "time": {"elapsed": 23},
  "_event_id": "5000_234_23_Goal_Normal Goal"  // Generated by monitor
}
```

**fixtures_active:** Enhanced events with debounce tracking (added in-place):
```javascript
{
  // Raw API fields (from live)
  "player": {"id": 234, "name": "D. Szoboszlai"},
  "team": {"id": 40, "name": "Liverpool"},
  "type": "Goal",
  "detail": "Normal Goal",
  "time": {"elapsed": 23},
  
  // Enhanced fields (added by debounce_job)
  "_event_id": "5000_234_23_Goal_Normal Goal",
  "_stable_count": 3,
  "_debounce_complete": true,
  "_twitter_complete": false,
  "_first_seen": "2025-11-24T15:23:45Z",
  "_snapshots": [
    {"timestamp": "2025-11-24T15:23:45Z", "hash": "abc123"},
    {"timestamp": "2025-11-24T15:24:45Z", "hash": "abc123"},
    {"timestamp": "2025-11-24T15:25:45Z", "hash": "abc123"}
  ],
  "_score_before": {"home": 0, "away": 0},
  "_score_after": {"home": 1, "away": 0},
  "_scoring_team": "home",
  "_twitter_search": "Szoboszlai Liverpool"
}
```

---

## 🔄 Pipeline Flow

```mermaid
sequenceDiagram
    participant Monitor
    participant Live as fixtures_live
    participant Active as fixtures_active
    participant Debounce
    participant Twitter
    
    Monitor->>Live: Store filtered API data<br/>with _event_id
    Monitor->>Monitor: Compare live vs active
    
    alt Case 1: NEW events
        Monitor->>Debounce: Trigger debounce
        Debounce->>Active: Add new event<br/>stable_count=1
    end
    
    alt Case 2: INCOMPLETE events
        Monitor->>Debounce: Trigger debounce
        Debounce->>Debounce: Compare hash
        alt Hash same
            Debounce->>Active: Increment stable_count
            alt stable_count >= 3
                Debounce->>Active: Mark debounce_complete
                Debounce->>Twitter: Trigger twitter job
            end
        else Hash changed
            Debounce->>Active: Reset stable_count=1
        end
    end
    
    alt Case 3: REMOVED events
        Monitor->>Debounce: Trigger debounce
        Debounce->>Active: Mark event removed
    end
```

### 1. Ingest Job (Daily 00:05 UTC)

Fetches today's fixtures and routes to appropriate collections:

```
1. Fetch fixtures from API-Football
2. Filter to 50 tracked teams
3. Route by status:
   - TBD/NS → fixtures_staging
   - LIVE → fixtures_active (with empty events array)
   - FT/AET/PEN → fixtures_completed
```

### 2. Monitor Job (Every Minute)

Activates fixtures and triggers debounce when needed:

```
1. Activate fixtures (staging → active with EMPTY events array)
2. Batch fetch fresh API data for ALL active fixtures
3. Filter events (Goals only) and store in fixtures_live WITH _event_id
4. Compare fixtures_live vs fixtures_active (3 trigger cases)
5. If needs_debounce: directly invoke debounce_job(fixture_id)
6. After debounce: Move FT/AET/PEN fixtures to completed
```

**3 Cases That Trigger Debounce:**
- **NEW:** Event in live but NOT in active
- **INCOMPLETE:** Event in both but `_debounce_complete=false`
- **REMOVED:** Event in active but NOT in live (VAR disallowed)

### 3. Debounce Job (Per Fixture)

Processes events using clean iteration pattern:

```
1. Get live events (with _event_id) and active events
2. Build dict of live events by _event_id
3. Iterate active events:
   
   IF event_id in live_events_dict:
     Pop event from dict
     
     IF hash unchanged:
       → Increment stable_count
       → Add snapshot
       → If stable_count >= 3: mark debounce_complete, trigger twitter
     ELSE:
       → Reset stable_count = 1 (hash changed)
   
   ELSE (not in live):
     → Mark event as removed (VAR case)

4. Whatever's left in live_events_dict = NEW events
   → Add to active with stable_count=1
```

**Hash Fields:** `player_id`, `team_id`, `type`, `detail`, `time_elapsed`, `assist_id`

### 4. Twitter Job (Per Event)

Called when `_debounce_complete=true`:

```
1. Get event from fixtures_active.events array
2. Use prebuilt _twitter_search field
3. Search Twitter for videos
4. Download videos to MinIO
5. Mark _twitter_complete=true
```

---

## 📊 MongoDB Collections (4 Total)

### fixtures_staging

Fixtures waiting to start (status TBD, NS).

```json
{
  "_id": 5000,
  "fixture": {
    "id": 5000,
    "date": "2025-11-24T15:00:00Z",
    "status": {"short": "TBD"}
  },
  "teams": {
    "home": {"id": 40, "name": "Liverpool"},
    "away": {"id": 50, "name": "Man City"}
  }
}
```

### fixtures_live

**Temporary storage** for raw API data with filtered events (Goals only). Gets **overwritten** each poll.

```json
{
  "_id": 5000,
  "stored_at": "2025-11-24T15:25:00Z",
  "fixture": {...},
  "teams": {...},
  "events": [
    {
      "player": {"id": 234, "name": "D. Szoboszlai"},
      "team": {"id": 40, "name": "Liverpool"},
      "type": "Goal",
      "detail": "Normal Goal",
      "time": {"elapsed": 23},
      "_event_id": "5000_234_23_Goal_Normal Goal"  // Generated by monitor
    }
  ]
}
```

### fixtures_active

Enhanced fixtures with debounce tracking. Events array **grows incrementally**, **never replaced**.

```json
{
  "_id": 5000,
  "activated_at": "2025-11-24T15:00:00Z",
  "fixture": {...},
  "teams": {...},
  "events": [
    {
      // Raw API fields
      "player": {"id": 234, "name": "D. Szoboszlai"},
      "team": {"id": 40, "name": "Liverpool"},
      "type": "Goal",
      "detail": "Normal Goal",
      "time": {"elapsed": 23},
      
      // Enhanced fields (added by debounce_job, never overwritten)
      "_event_id": "5000_234_23_Goal_Normal Goal",
      "_stable_count": 3,
      "_debounce_complete": true,
      "_twitter_complete": false,
      "_first_seen": "2025-11-24T15:23:45Z",
      "_snapshots": [
        {"timestamp": "2025-11-24T15:23:45Z", "hash": "abc123"},
        {"timestamp": "2025-11-24T15:24:45Z", "hash": "abc123"},
        {"timestamp": "2025-11-24T15:25:45Z", "hash": "abc123"}
      ],
      "_score_before": {"home": 0, "away": 0},
      "_score_after": {"home": 1, "away": 0},
      "_scoring_team": "home",
      "_twitter_search": "Szoboszlai Liverpool"
    }
  ]
}
```

### fixtures_completed

Archive of finished fixtures with all enhancements intact. fixtures_live entry is deleted when moved here.

---

## 🔌 Port Configuration

**Development Access (via SSH forwarding):**
- **Dagster UI:** http://localhost:3100
- **MongoDB Express:** http://localhost:3101  
- **MinIO Console:** http://localhost:3102
- **Twitter VNC:** http://localhost:6080/vnc.html

**Internal Services:**
- PostgreSQL: `postgres:5432`
- MongoDB: `mongo:27017`
- MinIO API: `minio:9000`

---

## 🚀 Getting Started

### Prerequisites

- Docker & Docker Compose
- Python 3.11+
- SSH access to server (for port forwarding)

### Quick Start

```bash
# 1. Clone repo
git clone <repo-url>
cd found-footy

# 2. Set up environment
cp .env.example .env
# Edit .env with your API-Football key

# 3. Start services
docker-compose up -d

# 4. SSH port forwarding (from local machine)
ssh -L 3100:localhost:3100 -L 3101:localhost:3101 -L 3102:localhost:3102 user@server

# 5. Access Dagster UI
# Open http://localhost:3100 in your browser
```

---

## 📂 Project Structure

```
found-footy/
├── src/
│   ├── data/
│   │   ├── mongo_store.py       # 4 collections (staging/live/active/completed)
│   │   └── s3_store.py          # MinIO video storage
│   ├── jobs/
│   │   ├── ingest/              # Daily fixture ingestion
│   │   ├── monitor/             # Per-minute monitoring + comparison
│   │   ├── debounce/            # Per-fixture event validation (clean iteration)
│   │   ├── twitter/             # Per-event video discovery
│   │   └── download/            # Video download & dedup
│   ├── utils/
│   │   └── event_config.py      # Event filtering config (Goals only)
│   └── api/
│       └── mongo_api.py         # API-Football wrapper
├── docker-compose.yml
├── Dockerfile
└── README.md
```

---

## 🎯 Key Features

- **4-Collection Architecture** - Staging → Live (temp) → Active (enhanced) → Completed
- **In-Place Enhancement** - Events enhanced incrementally, never overwritten
- **Clean Iteration Pattern** - Build dict, pop as processed, leftovers are NEW
- **Event Filtering** - Only Goals stored (Normal Goal, Penalty, Own Goal)
- **Hash-Based Stability** - MD5 of critical fields, 3 consecutive stable polls
- **3 Debounce Trigger Cases** - NEW, INCOMPLETE, REMOVED (VAR)
- **Pre-Generated Event IDs** - Set in monitor for clean comparison
- **Smart Twitter Search** - Player last name + team name
- **MinIO Storage** - S3-compatible local video storage
- **Dagster Orchestration** - Visual pipeline monitoring

---

## 🐛 Debugging

### Check Active Fixtures

```bash
# MongoDB Express
http://localhost:3101

# Click: found_footy → fixtures_active
# Look at events array for enhanced fields
```

### Check Dagster Logs

```bash
# Dagster UI
http://localhost:3100

# Click: Runs → Select job → View logs
```

### Manual Job Triggers

```python
# In Dagster UI, go to Jobs and click "Launch Run"
# Provide config for twitter_job:
{
  "ops": {
    "search_and_save_twitter_videos": {
      "config": {
        "fixture_id": 5000,
        "event_id": "5000_234_Goal_1"
      }
    }
  }
}
```

---

## 📝 Notes

- **API Limit:** 7500 requests/day (Pro plan)
- **Batch Endpoint:** Gets fixtures + events + lineups in one call
- **Debounce Window:** 3 polls at 1-minute intervals
- **Twitter Retry:** 2min initial + 3min + 4min (total ~10min)
- **Storage:** MinIO (S3-compatible) at `minio:9000`

---

## 🔮 Future Enhancements

- [ ] Actual Twitter integration (currently placeholder)
- [ ] Video download & deduplication with OpenCV
- [ ] Frontend UI to query fixtures_active
- [ ] Webhook notifications for completed events
- [ ] Multi-league support beyond 50 teams

---

## 📜 License

MIT
