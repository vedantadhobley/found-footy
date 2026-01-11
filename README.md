# ⚽ Found Footy

**Automated football goal video pipeline** — Detects goals in real-time, discovers videos on Twitter, downloads and deduplicates with perceptual hashing, and stores the best quality videos in S3.

Built with **Temporal.io** for orchestration, **MongoDB** for state management, and **Firefox browser automation** for Twitter scraping.

---

## 🎯 What It Does

```mermaid
flowchart TB
    subgraph INPUT["📡 Input"]
        API[API-Football<br/>Live match data]
    end
    
    subgraph DETECT["🔍 Detection"]
        GOAL[Goal Detected!<br/>Player scores]
        STABLE[Stable after 3 polls<br/>~3 minutes]
    end
    
    subgraph DISCOVER["🐦 Discovery"]
        TWITTER[Twitter Search<br/>3 attempts × 5 videos]
    end
    
    subgraph PROCESS["⚙️ Processing"]
        DOWNLOAD[Download via yt-dlp]
        FILTER[Filter >3s to 60s duration]
        AI[AI Vision Validation<br/>Is this soccer?]
        HASH[Perceptual Hash<br/>Dense 0.25s sampling]
        DEDUP[Deduplicate<br/>Keep best quality]
    end
    
    subgraph OUTPUT["💾 Output"]
        S3[MinIO S3<br/>Ranked videos]
        META[MongoDB<br/>Event metadata]
    end
    
    API --> GOAL --> STABLE
    STABLE --> TWITTER
    TWITTER --> DOWNLOAD --> FILTER --> AI --> HASH --> DEDUP
    DEDUP --> S3
    DEDUP --> META
```

**The pipeline handles:**
- 🎯 **50 top European clubs** — Premier League, La Liga, Bundesliga, Serie A, Ligue 1, Champions League
- ⏱️ **Real-time detection** — Goals detected within minutes of scoring
- 🔄 **VAR handling** — Disallowed goals automatically detected and marked
- 📊 **Quality ranking** — Videos ranked by resolution, with duplicates removed
- 🏷️ **Rich metadata** — Score at time of goal, scorer, assister, display titles with highlights

---

## 🏗️ Architecture Overview

```mermaid
flowchart TB
    subgraph TEMPORAL["⏰ Temporal.io Orchestration"]
        direction TB
        INGEST[IngestWorkflow<br/>Daily 00:05 UTC]
        MONITOR[MonitorWorkflow<br/>Every minute]
        RAGWF[RAGWorkflow<br/>Team alias lookup]
        TWITTERWF[TwitterWorkflow<br/>3× per event]
        DOWNLOADWF[DownloadWorkflow<br/>Per video batch]
        
        INGEST -.->|Pre-caches aliases| RAGWF
        INGEST -.->|Populates| MONITOR
        MONITOR -->|Triggers| RAGWF
        RAGWF -->|Triggers| TWITTERWF
        TWITTERWF -->|Triggers| DOWNLOADWF
    end
    
    subgraph STORAGE["💾 Storage Layer"]
        direction TB
        MONGO[(MongoDB<br/>5 Collections)]
        MINIO[(MinIO S3<br/>Video files)]
    end
    
    subgraph EXTERNAL["🌐 External Services"]
        direction TB
        APIFB[API-Football<br/>Match data]
        WIKIDATA[Wikidata<br/>Team aliases]
        LLM[llama.cpp LLM<br/>Alias selection + Vision]
        TWITTER[Twitter/X<br/>Video discovery]
    end
    
    INGEST --> APIFB
    MONITOR --> APIFB
    MONITOR --> MONGO
    RAGWF --> WIKIDATA
    RAGWF --> LLM
    TWITTERWF --> TWITTER
    DOWNLOADWF --> LLM
    DOWNLOADWF --> MINIO
    DOWNLOADWF --> MONGO
```

---

## 📊 Data Flow

### MongoDB 5-Collection Architecture

```mermaid
flowchart TB
    subgraph COLLECTIONS["MongoDB Collections"]
        direction LR
        STAGING[(fixtures_staging<br/>Upcoming matches)]
        LIVE[(fixtures_live<br/>Temp comparison)]
        ACTIVE[(fixtures_active<br/>In-progress)]
        COMPLETED[(fixtures_completed<br/>Archive)]
    end
    
    API[API-Football] -->|Daily ingest| STAGING
    STAGING -->|Start time reached| ACTIVE
    API -->|Every minute| LIVE
    LIVE <-->|Compare events| ACTIVE
    ACTIVE -->|Match finished| COMPLETED
    LIVE -.->|Deleted after| COMPLETED
    
    style STAGING fill:#e1f5ff,color:#000
    style LIVE fill:#fff4e1,color:#000
    style ACTIVE fill:#e1ffe1,color:#000
    style COMPLETED fill:#f0f0f0,color:#000
```

| Collection | Purpose | Lifecycle |
|------------|---------|-----------|
| **fixtures_staging** | Matches waiting to start (TBD, NS) | Hours to days |
| **fixtures_live** | Raw API data for comparison | ~1 minute (overwritten) |
| **fixtures_active** | Enhanced events with video tracking | ~90 minutes |
| **fixtures_completed** | Permanent archive | Forever |
| **team_aliases** | Cached team aliases from RAG pipeline | Persistent |

### Event Lifecycle

```mermaid
flowchart TB
    NEW[🆕 New Event<br/>Goal detected in API]
    COUNT1[stable_count = 1]
    COUNT2[stable_count = 2]
    COUNT3[stable_count = 3<br/>✅ STABLE]
    
    TWITTER1[🐦 Twitter Search #1<br/>Find early uploads]
    TWITTER2[🐦 Twitter Search #2<br/>+5 min, find more]
    TWITTER3[🐦 Twitter Search #3<br/>+5 min, final search]
    
    DOWNLOAD[⬇️ Download Videos<br/>Filter & dedupe]
    COMPLETE[✅ Event Complete<br/>_twitter_complete = true]
    
    NEW --> COUNT1
    COUNT1 -->|+1 min poll| COUNT2
    COUNT2 -->|+1 min poll| COUNT3
    COUNT3 --> TWITTER1
    TWITTER1 --> DOWNLOAD
    TWITTER1 -.->|+5 min| TWITTER2
    TWITTER2 --> DOWNLOAD
    TWITTER2 -.->|+5 min| TWITTER3
    TWITTER3 --> DOWNLOAD
    TWITTER3 --> COMPLETE
    
    VAR[❌ VAR Disallowed<br/>Event disappears from API]
    COUNT1 & COUNT2 -.->|Event removed| VAR
    
    style COUNT3 fill:#e8f5e9,color:#000
    style COMPLETE fill:#e8f5e9,color:#000
    style VAR fill:#ffebee,color:#000
```

---

## 🔧 Core Features

### Set-Based Event Debounce

Events are identified by a unique ID: `{fixture_id}_{team_id}_{player_id}_{event_type}_{sequence}`

```python
# Pure set operations - O(1) lookups, no hash comparison needed
live_ids = {e["_event_id"] for e in live_events}
active_ids = {e["_event_id"] for e in active_events}

new_ids = live_ids - active_ids       # NEW → add with count=1
removed_ids = active_ids - live_ids   # VAR → mark _removed=true
matching_ids = live_ids & active_ids  # EXISTS → increment count
```

**Why this works:**
- If VAR changes the scorer → Different player_id → Different event_id → Detected automatically
- No MD5 hashing or deep comparison needed
- Simple, fast, reliable

### Twitter Video Discovery

```mermaid
flowchart TB
    subgraph ATTEMPT["Each Twitter Attempt"]
        SEARCH[Search query:<br/>Salah Liverpool]
        EXCLUDE[Pass exclude_urls<br/>Skip already found]
        RESULTS[Get up to 5 NEW videos]
    end
    
    subgraph STRATEGY["3-Attempt Strategy"]
        A1[Attempt 1: Immediately<br/>Early uploads, often SD]
        A2[Attempt 2: +5 minutes<br/>More uploads available]
        A3[Attempt 3: +5 minutes<br/>Best quality uploads]
    end
    
    A1 --> SEARCH
    A2 --> SEARCH
    A3 --> SEARCH
    SEARCH --> EXCLUDE --> RESULTS
    
    RESULTS -->|Up to 15 unique videos| TOTAL[Total: 5 x 3 = 15 videos]
```

**Key feature: URL Exclusion**
- Each search passes `exclude_urls` containing previously discovered videos
- Twitter service skips these during scraping
- Result: 15 unique videos instead of the same 5 repeated

### Perceptual Hash Deduplication

**Problem:** Same video at different resolutions = different file hashes. Also, videos of the same goal often start at different times.

**Solution:** Dense sampling with histogram equalization

```mermaid
flowchart LR
    subgraph COMPUTE["Compute Hash"]
        direction TB
        SAMPLE[Sample every 0.25s]
        EQUAL[Histogram equalize]
        HASH[dHash 64-bit per frame]
    end
    
    RESULT["dense:0.25:0.25=abc123,0.50=def456,..."]
    
    SAMPLE --> EQUAL --> HASH --> RESULT
```

**Hash format:** `dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,...`

**Matching algorithm:**
- Tries all possible time offsets between videos
- Requires **3 consecutive frames** to match at consistent offset
- Each frame: Hamming distance ≤10 bits (of 64)
- Histogram equalization handles color/brightness differences

**Why 3 consecutive?** Single-frame matching causes false positives between similar content (e.g., two goals in same match).

### Quality Ranking

Videos are ranked by:
1. **Popularity** — How many sources uploaded the same content (same perceptual hash)
2. **Resolution** — Width × Height (1080p > 720p > 480p)

```mermaid
flowchart TB
    subgraph VIDEOS["Downloaded Videos"]
        V1[Video A<br/>720p, hash: abc123]
        V2[Video B<br/>1080p, hash: abc123]
        V3[Video C<br/>720p, hash: abc123]
        V4[Video D<br/>1080p, hash: xyz789]
    end
    
    subgraph RANKED["Ranked Result"]
        R1[Rank 1: Video B<br/>1080p, popularity=3]
        R2[Rank 2: Video D<br/>1080p, popularity=1]
    end
    
    V1 & V2 & V3 -->|Same hash, keep best| R1
    V4 --> R2
```

### Display Titles with Highlights

Events include display-ready titles with `<<>>` markers for frontend highlighting:

```javascript
// Title: Scoring team highlighted
"<<Liverpool (3)>> - 0 Arsenal"
"Manchester City 1 - <<(2) Real Madrid>>"

// Subtitle: Scorer highlighted
"88' Goal - <<A. Grimaldo>> (Florian Wirtz)"
"13' Own Goal - <<Bruno Guimaraes>> (R. Andrich)"
"51' Penalty Goal - <<A. Gordon>>"
```

**Score context:** Title shows score *at time of goal*, not final score.

---

## 🐳 Infrastructure

### Services

```mermaid
flowchart TB
    subgraph DOCKER["Docker Compose Stack"]
        direction TB
        
        subgraph CORE["Core Services"]
            WORKER[Worker<br/>Temporal activities]
            TEMPORAL[Temporal Server<br/>Workflow orchestration]
            POSTGRES[(PostgreSQL<br/>Temporal metadata)]
        end
        
        subgraph DATA["Data Layer"]
            MONGO[(MongoDB<br/>Application data)]
            MINIO[(MinIO<br/>S3-compatible storage)]
        end
        
        subgraph TWITTER["Twitter Automation"]
            TWITTERSVC[Twitter Service<br/>FastAPI :8888]
            FIREFOX[Firefox + Selenium]
            VNC[noVNC :6080<br/>Browser GUI access]
        end
        
        subgraph UI["Management UIs"]
            TEMPUI[Temporal UI :8080]
            MONGOKU[Mongoku :3100]
            MINIOUI[MinIO Console :9001]
        end
    end
    
    WORKER --> TEMPORAL
    TEMPORAL --> POSTGRES
    WORKER --> MONGO
    WORKER --> MINIO
    WORKER --> TWITTERSVC
    TWITTERSVC --> FIREFOX
    FIREFOX --> VNC
```

### Port Allocation

| Service | Dev Port | Prod Port | Purpose |
|---------|----------|-----------|---------|
| Temporal UI | 4100 | 3100 | Workflow monitoring |
| Mongoku | 4101 | 3101 | MongoDB GUI |
| MinIO Console | 4102 | 3102 | S3 management |
| Twitter VNC | 4103 | 3103 | Browser access |
| Temporal gRPC | 7233 | 7233 | Workflow API |

### Internal Services (Docker network only)

| Service | Port | Purpose |
|---------|------|---------|
| MongoDB | 27017 | Application data |
| PostgreSQL | 5432 | Temporal metadata |
| MinIO API | 9000 | S3 storage |
| Twitter API | 8888 | Search endpoint |

---

## 🚀 Getting Started

### Prerequisites

- Docker & Docker Compose
- API-Football API key ([api-football.com](https://api-football.com))
- Twitter/X account for video discovery

### Quick Start

```bash
# 1. Clone and configure
git clone <repo-url>
cd found-footy
cp .env.example .env
# Edit .env with your API-Football key

# 2. Start services
docker compose -f docker-compose.dev.yml up -d

# 3. First-time Twitter login
# Open http://localhost:4103 (VNC browser)
# Log into Twitter in the Firefox window
# Cookies are saved automatically

# 4. Verify health
curl http://localhost:8888/health
# Should return: {"status": "healthy", "authenticated": true}

# 5. Access UIs (via SSH tunnel if remote)
ssh -L 4100:localhost:4100 -L 4101:localhost:4101 \
    -L 4102:localhost:4102 -L 4103:localhost:4103 user@server

# Temporal UI: http://localhost:4100
# MongoDB:     http://localhost:4101
# MinIO:       http://localhost:4102
```

### Test the Pipeline

```bash
# Insert a test fixture
docker exec found-footy-dev-worker python \
    /workspace/tests/workflows/test_pipeline.py --fixture-id 1469132

# Watch the pipeline
docker compose -f docker-compose.dev.yml logs -f worker
```

---

## 📂 Project Structure

```
found-footy/
├── src/
│   ├── workflows/              # Temporal workflow definitions
│   │   ├── ingest_workflow.py  # Daily fixture ingestion
│   │   ├── monitor_workflow.py # Event detection & debounce
│   │   ├── twitter_workflow.py # Video discovery
│   │   └── download_workflow.py # Download & upload pipeline
│   │
│   ├── activities/             # Temporal activity implementations
│   │   ├── ingest.py           # API-Football fetching
│   │   ├── monitor.py          # Event processing
│   │   ├── twitter.py          # Twitter search
│   │   ├── download.py         # Video download/upload
│   │   └── rag.py              # Team alias RAG (Wikidata + LLM)
│   │
│   ├── data/
│   │   ├── mongo_store.py      # 5-collection MongoDB
│   │   └── s3_store.py         # MinIO video storage
│   │
│   ├── utils/
│   │   ├── event_config.py     # Event filtering rules
│   │   ├── event_enhancement.py # Score context calculation
│   │   └── team_data.py        # 50 tracked teams
│   │
│   └── worker.py               # Temporal worker entry point
│
├── twitter/                    # Browser automation service
│   ├── app.py                  # FastAPI server
│   ├── session.py              # Selenium browser session
│   ├── auth.py                 # Cookie management
│   └── start_with_vnc.sh       # VNC startup script
│
├── tests/
│   └── workflows/
│       ├── test_pipeline.py    # End-to-end test
│       └── test_ingest.py      # Ingest workflow test
│
├── docker-compose.dev.yml      # Development stack
├── docker-compose.yml          # Production stack
├── Dockerfile                  # Worker image
└── Dockerfile.dev              # Twitter service image
```

---

## ⏰ Workflow Details

### 1. IngestWorkflow

**Schedule:** Daily at 00:05 UTC  
**Purpose:** Fetch today's fixtures, pre-cache team aliases, route by status

```mermaid
flowchart TB
    TRIGGER[⏰ Daily 00:05 UTC]
    FETCH[Fetch fixtures<br/>GET /fixtures?date=today]
    RAG[Pre-cache RAG aliases<br/>Both teams per fixture]
    
    subgraph ROUTE["Route by Status"]
        TBD[TBD, NS → fixtures_staging]
        LIVE[1H, HT, 2H → fixtures_active]
        DONE[FT, AET, PEN → fixtures_completed]
    end
    
    TRIGGER --> FETCH --> RAG --> TBD & LIVE & DONE
```

**Pre-caching:** Calls `get_team_aliases` for both home and away teams. This ensures aliases are ready before any goals are scored, including for opponent teams (non-tracked teams).

### 2. MonitorWorkflow

**Schedule:** Every minute  
**Purpose:** Activate fixtures, detect events, trigger RAG → Twitter pipeline

```mermaid
flowchart TB
    TRIGGER[⏰ Every minute]
    
    ACTIVATE[Activate ready fixtures<br/>staging → active]
    FETCH[Fetch active fixtures<br/>from API-Football]
    STORE[Store in fixtures_live<br/>with _event_id]
    PROCESS[Process events<br/>Set operations]
    
    subgraph EVENTS["Event Handling"]
        NEW[NEW: Add with count=1]
        MATCH[MATCH: Increment count]
        REMOVE[REMOVED: Mark _removed]
    end
    
    RAG[🤖 Trigger RAGWorkflow<br/>Fire-and-forget]
    SYNC[Sync fixture metadata<br/>Update API fields]
    COMPLETE[Complete if finished<br/>active → completed]
    
    TRIGGER --> ACTIVATE --> FETCH --> STORE --> PROCESS
    PROCESS --> NEW & MATCH & REMOVE
    MATCH -->|count=3| RAG
    PROCESS --> SYNC --> COMPLETE
```

### 3. RAGWorkflow

**Trigger:** Per stable event (fired by Monitor)  
**Purpose:** Resolve team aliases via Wikidata + LLM, trigger Twitter

```mermaid
flowchart TB
    TRIGGER[Triggered by Monitor<br/>Fire-and-forget]
    
    CACHE{Cache hit?}
    CACHED[Return cached aliases<br/>team_aliases collection]
    
    subgraph RAG["Full RAG Pipeline"]
        WIKI[Query Wikidata<br/>Search for team QID]
        FETCH[Fetch aliases<br/>English only]
        PREPROCESS[Preprocess to words<br/>Filter junk]
        LLM[llama.cpp LLM selects<br/>Best search terms]
    end
    
    SAVE[Save to event<br/>_twitter_aliases]
    TWITTER[🐦 Trigger TwitterWorkflow<br/>Child workflow]
    
    TRIGGER --> CACHE
    CACHE -->|Yes| CACHED --> SAVE
    CACHE -->|No| WIKI --> FETCH --> PREPROCESS --> LLM --> SAVE
    SAVE --> TWITTER
```

**Alias Examples:**
- `"Liverpool"` → `["Liverpool", "LFC", "Reds", "Anfield"]`
- `"Atletico Madrid"` → `["Atletico", "Madrid", "ATM", "Atleti"]`
- `"Belgium"` (national) → `["Belgium", "Belgian", "Belgique"]`

### 4. TwitterWorkflow

**Trigger:** Per stable event (runs 3 times)  
**Purpose:** Search Twitter with aliases, trigger download

```mermaid
flowchart TB
    TRIGGER[Triggered by RAGWorkflow<br/>Child workflow]
    
    GET[Get search data<br/>Query + exclude_urls]
    SEARCH[POST to twitter:8888<br/>Firefox automation]
    SAVE[Save discovered videos<br/>to MongoDB]
    
    FOUND{Videos found?}
    DOWNLOAD[Trigger DownloadWorkflow<br/>Blocking child]
    DONE[Done]
    
    TRIGGER --> GET --> SEARCH --> FOUND
    FOUND -->|Yes| SAVE --> DOWNLOAD --> DONE
    FOUND -->|No| DONE
```

### 5. DownloadWorkflow

**Trigger:** Per Twitter search with videos  
**Purpose:** Download, validate, deduplicate, upload

```mermaid
flowchart TB
    TRIGGER[Triggered by Twitter<br/>With video URLs]
    
    FETCH[Fetch event data<br/>Existing S3 videos from MongoDB]
    
    subgraph DOWNLOAD["1. Download Phase"]
        DL[Download via yt-dlp]
        FILTER{Duration<br/>>3-60s?}
        META[Extract metadata<br/>Resolution, bitrate]
        FILTERED[Track as filtered]
    end
    
    subgraph VALIDATE["2. AI Validation"]
        AI{Vision Model<br/>Is soccer?}
        REJECT[Reject non-soccer]
    end
    
    subgraph HASH["3. Hash Generation"]
        PHASH[Compute perceptual hash<br/>Dense 0.25s sampling]
    end
    
    subgraph DEDUP["4. Deduplication"]
        COMPARE[Compare hashes<br/>3 consecutive frames]
        QUALITY[Keep best quality]
    end
    
    subgraph UPLOAD["5. Upload Phase"]
        REPLACE[Replace if better]
        NEW[Upload new videos]
    end
    
    SAVE[Save to MongoDB<br/>_s3_videos array]
    
    TRIGGER --> FETCH --> DL
    DL --> FILTER
    FILTER -->|Pass| META --> AI
    FILTER -->|Fail| FILTERED
    AI -->|Soccer| PHASH
    AI -->|Not soccer| REJECT
    PHASH --> COMPARE --> QUALITY
    QUALITY --> REPLACE & NEW --> SAVE
```

**Optimized Pipeline Order**: AI validation runs BEFORE perceptual hash generation. This saves expensive hash computation (dense 0.25s sampling with ffmpeg) for non-soccer videos that would be rejected anyway.

**AI Video Validation**: Vision model (Qwen3-VL via llama.cpp) validates each video contains soccer content. Uses fail-closed policy - if AI unavailable, video is skipped.

---

## 🔁 Retry Strategy

All activities have exponential backoff:

| Activity Type | Max Retries | Initial Wait | Backoff |
|--------------|-------------|--------------|---------|
| MongoDB reads | 2-3 | 1s | 2.0× |
| MongoDB writes | 3 | 1s | 2.0× |
| API-Football | 3 | 1s | 2.0× |
| Twitter search | 3 | 10s | 1.5× |
| Video download | 3 | 2s | 2.0× |
| AI validation | 4 | 3s | 2.0× |
| S3 upload | 3 | 2s | 1.5× |

---

## 📊 Event Schema

Events are stored with both raw API fields and enhancement fields:

```javascript
{
  // ═══════════ RAW API FIELDS ═══════════
  "player": {"id": 306, "name": "Mohamed Salah"},
  "team": {"id": 40, "name": "Liverpool"},
  "assist": {"id": 123, "name": "Trent Alexander-Arnold"},
  "type": "Goal",
  "detail": "Normal Goal",  // or "Own Goal", "Penalty"
  "time": {"elapsed": 45, "extra": 3},
  
  // ═══════════ ENHANCEMENT FIELDS ═══════════
  "_event_id": "123456_40_306_Goal_1",
  "_stable_count": 3,
  "_monitor_complete": true,
  "_twitter_count": 3,
  "_twitter_complete": true,
  "_twitter_search": "Salah Liverpool",
  "_first_seen": "2025-01-01T15:45:00Z",
  "_removed": false,
  
  // ═══════════ SCORE CONTEXT (for frontend title generation) ═══════════
  "_score_after": {"home": 3, "away": 0},
  "_scoring_team": "home",
  
  // ═══════════ VIDEO TRACKING ═══════════
  "_discovered_videos": [
    {"video_page_url": "https://x.com/...", "tweet_url": "..."}
  ],
  "_s3_videos": [
    {
      "url": "/video/footy-videos/123456/.../abc123.mp4",
      "perceptual_hash": "15.2:4c33b33b:f8d2d234:48b2a460",
      "resolution_score": 2073600,  // 1920×1080
      "popularity": 3,
      "rank": 1
    }
  ]
}
```

---

## 🐛 Debugging

### Check Workflow Status
```bash
# Temporal UI
open http://localhost:4100
```

### Check Event Data
```bash
# MongoDB (via Mongoku)
open http://localhost:4101
# Navigate: found_footy → fixtures_active → events array
```

### Check S3 Videos
```bash
docker exec found-footy-dev-worker python -c "
from src.data.s3_store import FootyS3Store
s3 = FootyS3Store()
for obj in s3.s3_client.list_objects_v2(Bucket='footy-videos').get('Contents', []):
    print(f\"{obj['Key']} ({obj['Size']/1024/1024:.1f} MB)\")
"
```

### Common Issues

| Symptom | Cause | Fix |
|---------|-------|-----|
| Fixture stuck in active | Events missing `_twitter_complete` | Check worker logs for Twitter errors |
| Twitter search empty | Session expired | Re-login via VNC (port 4103) |
| Videos not uploading | S3 connection failed | Check MinIO is running |
| Same videos repeatedly | `exclude_urls` not passed | Check TwitterWorkflow activities |

---

## 📝 Configuration

### Environment Variables

```bash
# API-Football
API_FOOTBALL_KEY=your_api_key

# MongoDB
MONGODB_URI=mongodb://user:pass@mongo:27017/found_footy

# MinIO S3
S3_ENDPOINT=http://minio:9000
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_BUCKET=footy-videos

# Temporal
TEMPORAL_ADDRESS=temporal:7233

# Twitter Service
TWITTER_SERVICE_URL=http://twitter:8888
```

### Limits & Thresholds

| Parameter | Value | Notes |
|-----------|-------|-------|
| API daily limit | 7,500 requests | Pro plan |
| Debounce polls | 3 | ~1.5 minutes (3 × 30s) |
| Twitter searches | 3 per event | ~15 min window |
| Videos per search | 5 max | |
| Duration filter | >3s to 60s | Must be strictly >3s |
| Download timeout | 60s per video | |
| Tracked teams | 50 | Top European clubs |
| Aspect ratio min | 1.33 (4:3) | Rejects vertical |

---

## 📚 Additional Documentation

- **[ARCHITECTURE.md](./ARCHITECTURE.md)** — Detailed collection schemas, workflow internals
- **[TEMPORAL_WORKFLOWS.md](./TEMPORAL_WORKFLOWS.md)** — Activity specifications, retry policies
- **[TWITTER_AUTH.md](./TWITTER_AUTH.md)** — Browser automation, cookie management

---

## 📜 License

MIT
