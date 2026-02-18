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
        STABLE[Stable after 3 polls<br/>~90 seconds]
    end
    
    subgraph DISCOVER["🐦 Discovery"]
        TWITTER[Twitter Search<br/>10 attempts × 5 videos]
    end
    
    subgraph PROCESS["⚙️ Processing"]
        DOWNLOAD[Download via syndication API]
        FILTER[Filter 3-60s duration<br/>Aspect ratio ≥1.33]
        AI[AI Vision Validation<br/>Qwen3-VL-8B via llama.cpp]
        CLOCK[Clock Extraction<br/>±3 min tolerance]
        HASH[Perceptual Hash<br/>Dense 0.25s sampling]
        DEDUP[Scoped Dedup<br/>Verified vs unverified pools]
    end
    
    subgraph OUTPUT["💾 Output"]
        S3[MinIO S3<br/>Ranked videos]
        META[MongoDB<br/>Event metadata]
    end
    
    API --> GOAL --> STABLE
    STABLE --> TWITTER
    TWITTER --> DOWNLOAD --> FILTER --> AI --> CLOCK --> HASH --> DEDUP
    DEDUP --> S3
    DEDUP --> META
```

**The pipeline handles:**
- 🎯 **96+ top-5 league teams** — All clubs from Premier League, La Liga, Bundesliga, Serie A, Ligue 1 (dynamically tracked via API)
- 🌍 **15 national teams** — Top FIFA-ranked nations
- ⏱️ **Real-time detection** — Goals detected within minutes of scoring
- 🔄 **VAR handling** — Disallowed goals automatically detected and marked
- 📊 **Quality ranking** — Videos ranked by resolution, with duplicates removed
- 🏷️ **Rich metadata** — Score at time of goal, scorer, assister, display titles with highlights

---

## 🏗️ Architecture Overview

```mermaid
flowchart TB
    subgraph INFRA["Infrastructure (always running)"]
        TEMPORAL[Temporal Server]
        POSTGRES[(PostgreSQL)]
        MONGO[(MongoDB)]
        MINIO[(MinIO S3)]
        SCALER[Scaler Service]
    end

    subgraph MANAGED["Auto-Scaled (2–8 instances)"]
        WORKERS[Worker Pool<br/>6 workflows, 42 activities]
        TWITTER[Twitter Pool<br/>Firefox automation]
    end

    subgraph EXTERNAL["External Services"]
        APIFB[API-Football]
        WIKIDATA[Wikidata]
        LLM[llama.cpp<br/>Vision + LLM]
    end

    SCALER -->|Scale up/down| MANAGED
    WORKERS <--> TEMPORAL
    WORKERS --> MONGO & MINIO
    WORKERS --> TWITTER
    WORKERS --> LLM
    TEMPORAL --> POSTGRES
```

**Auto-Scaling Architecture**: Python's GIL limits each process to one CPU core. The **Scaler Service** automatically manages **2–8 worker replicas** based on Temporal task queue depth. Each worker handles 10 workflow tasks and 30 activities concurrently. All workers share a temp volume at `/tmp/found-footy` for video processing.

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
    
    API[API-Football] -->|Daily 3-day ingest| STAGING
    STAGING -->|Start time reached| ACTIVE
    API -->|Every 30 seconds| LIVE
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
| **fixtures_completed** | Permanent archive | 14 days (retention) |
| **team_aliases** | Cached team aliases from RAG pipeline | Persistent |

### Tracked Teams

**Dynamic Top-5 League Tracking**: Teams are fetched from API-Football for the current season and cached for 24 hours.

| League | Country | Teams |
|--------|---------|-------|
| Premier League | England | 20 |
| La Liga | Spain | 20 |
| Bundesliga | Germany | 18 |
| Serie A | Italy | 20 |
| Ligue 1 | France | 18 |
| **Total Clubs** | | **~96** |

Plus **15 national teams** (static): Spain, Argentina, France, England, Brazil, Portugal, Netherlands, Belgium, Germany, Croatia, Morocco, Italy, Colombia, USA, Mexico.

### Event Lifecycle

```mermaid
flowchart TB
    NEW[🆕 New Event<br/>Goal detected in API]
    COUNT1[stable_count = 1]
    COUNT2[stable_count = 2]
    COUNT3[stable_count = 3<br/>✅ STABLE]
    
    subgraph TWITTER["🐦 Twitter Search (10 attempts)"]
        T1[Attempt 1: Immediate]
        T2[Attempt 2-9: +1 min each]
        T10[Attempt 10: Final]
    end
    
    DOWNLOAD[⬇️ Download Videos<br/>Filter, validate, dedupe]
    COMPLETE[✅ Event Complete<br/>_download_complete = true]
    
    NEW --> COUNT1
    COUNT1 -->|+30s poll| COUNT2
    COUNT2 -->|+30s poll| COUNT3
    COUNT3 --> T1
    T1 --> DOWNLOAD
    T1 -->|+1 min| T2
    T2 --> DOWNLOAD
    T2 -->|+1 min each| T10
    T10 --> DOWNLOAD
    T10 --> COMPLETE
    
    VAR[❌ VAR Disallowed<br/>Event disappears from API]
    COUNT1 & COUNT2 -.->|Event removed| VAR
    
    style COUNT3 fill:#e8f5e9,color:#000
    style COMPLETE fill:#e8f5e9,color:#000
    style VAR fill:#ffebee,color:#000
```

**10 Search Attempts**: Each event gets 10 Twitter search attempts, spaced ~1 minute apart. This captures:
- Early uploads (often lower quality)
- Peak uploads (~2-5 min after goal)
- Late uploads (often best quality)

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
    subgraph ALIASES["1. Resolve Team Aliases"]
        CACHE{Cache hit?}
        RAG[RAG Pipeline<br/>Wikidata + LLM]
        RESULT["['Liverpool', 'LFC', 'Reds']"]
        CACHE -->|Yes| RESULT
        CACHE -->|No| RAG --> RESULT
    end
    
    subgraph SEARCH["2. Build OR Query"]
        QUERY["(Salah OR Mohamed)<br/>(Liverpool OR LFC OR Reds)"]
    end
    
    subgraph STRATEGY["3. 10-Attempt Strategy (~10 min total)"]
        A1[Attempt 1: Immediate]
        A2[Attempts 2-9: +1 min each]
        A10[Attempt 10: Final]
    end

    RESULT --> QUERY
    QUERY --> A1 & A2 & A10
    A1 & A2 & A10 -->|Top 5 longest per attempt| TOTAL[Up to 50 unique videos]
```

**Key Features:**
- **OR-query search**: Single query per attempt — `"(Salah OR Mohamed) (Liverpool OR LFC OR Reds)"`
- **URL Exclusion**: Each search passes `exclude_urls` to skip already-discovered videos
- **10 attempts**: Captures early SD uploads through late HD uploads
- **Top 5 by duration**: Longest videos selected per attempt (prefer full replays)
- **Result**: Up to 50 unique videos per event (5 per attempt × 10 attempts)

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

Videos are ranked by (all descending):
1. **`timestamp_verified`** — AI-verified videos always rank above unverified
2. **Popularity** — How many sources uploaded the same content (same perceptual hash)
3. **File size** — Proxy for resolution quality (larger file = higher bitrate/resolution)

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
            TEMPORAL[Temporal Server]
            POSTGRES[(PostgreSQL)]
            SCALER[Scaler Service]
        end
        
        subgraph DATA["Data Layer"]
            MONGO[(MongoDB)]
            MINIO[(MinIO S3)]
            TEMP[/tmp/found-footy<br/>Shared temp volume/]
        end
        
        subgraph MANAGED["Auto-Scaled (profiles: managed)"]
            WORKERS[Workers 2–8]
            TWITTER[Twitter 2–8<br/>Firefox + Selenium]
        end
        
        subgraph UI["Management UIs"]
            TEMPUI[Temporal UI :3200]
            MONGOKU[Mongoku :3201]
            MINIOUI[MinIO Console :3202]
        end
    end
    
    SCALER -->|Query task queue| TEMPORAL
    SCALER -->|Query active goals| MONGO
    SCALER -->|Scale up/down| MANAGED
    WORKERS --> TEMPORAL
    WORKERS --> TEMP
    TEMPORAL --> POSTGRES
    WORKERS --> MONGO & MINIO & TWITTER
```

**Auto-Scaling**: Workers and Twitter instances use `profiles: ["managed"]` — they are not started by `docker compose up -d`. The Scaler Service queries Temporal queue depth (for workers) and MongoDB active goals (for Twitter) every 30 seconds, then scales accordingly.

### Auto-Scaling (Production)

The **Scaler Service** automatically scales workers and Twitter instances:

| Service | Metric Source | Scale Up | Scale Down |
|---------|---------------|----------|------------|
| **Workers** | Temporal task queue depth | > 5 pending tasks/worker | < 2 pending tasks/worker |
| **Twitter** | MongoDB active goals | > 2 goals/instance | < 1 goal/instance |

**Why different metrics?**
- Workers scale on **Temporal queue depth** (pending tasks backlog)
- Twitter scales on **active goals** (goals with `_monitor_complete=true` but `_download_complete=false`)
- Twitter searches complete quickly (~3s), so queue is always near-empty even when busy
- Active goals = actual workload indicator (each goal runs 10 searches over ~10 min)

| Config | Value |
|--------|-------|
| Instances | min=2, max=8 |
| Check interval | 30 seconds |
| Cooldown | 60 seconds between scaling actions |

```mermaid
flowchart LR
    subgraph STARTUP["docker compose up -d"]
        INFRA[Infrastructure<br/>postgres, mongo, temporal, minio]
        SCALER[Scaler Service]
    end
    
    subgraph MANAGED["Managed by Scaler"]
        W[Workers 1-8]
        T[Twitter 1-8]
    end
    
    STARTUP --> SCALER
    SCALER -->|Query depth| TQ[Temporal Task Queue]
    SCALER -->|Query active goals| MONGO[(MongoDB)]
    SCALER -->|Auto-start/stop| MANAGED
```

**How it works:**
- `docker compose up -d` starts infrastructure + scaler only
- Scaler auto-starts minimum instances (2 workers, 2 twitter)
- All workers/twitter use `profiles: ["managed"]` — not started by default
- Scaler queries Temporal + MongoDB every 30 seconds
- Scales up when load is high, down when idle (with cooldown)
- Uses `python-on-whales` for clean Docker Compose integration
- Twitter instances checked for busy state before scale-down

| Service | Default | Max | Notes |
|---------|---------|-----|-------|
| Workers | 2 | 8 | Managed by scaler, uses `profiles: ["managed"]` |
| Twitter | 2 | 8 | twitter-1 has VNC, twitter-2+ are headless |
| Scaler | 1 | 1 | Always running, starts with `docker compose up -d` |

```bash
# Start entire stack (scaler will bring up workers/twitter)
docker compose up -d

# Rebuild all app images
docker compose build worker twitter scaler

# Check scaler logs (shows active goals + queue depth)
docker compose logs -f scaler

# Manual scaling (bypasses scaler, uses managed profile)
docker compose --profile managed up -d twitter-3 worker-3
```

**Twitter load balancing**: Each search randomly selects from healthy instances:
```python
healthy_urls = [url for url in all_twitter_urls if health_check(url)]
session_url = random.choice(healthy_urls)
```

### Port Allocation

| Service | Dev Port | Prod Port | Purpose |
|---------|----------|-----------|---------|
| Temporal UI | 4200 | 3200 | Workflow monitoring |
| Mongoku | 4201 | 3201 | MongoDB GUI |
| MinIO Console | 4202 | 3202 | S3 management |
| Twitter-1 VNC | 4203 | 3203 | Browser access (only instance with VNC) |
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
# Open http://localhost:4203 (VNC browser)
# Log into Twitter in the Firefox window
# Cookies are saved automatically

# 4. Verify health
curl http://localhost:8888/health
# Should return: {"status": "healthy", "authenticated": true}

# 5. Access UIs (via SSH tunnel if remote)
ssh -L 4200:localhost:4200 -L 4201:localhost:4201 \
    -L 4202:localhost:4202 -L 4203:localhost:4203 user@server

# Temporal UI: http://localhost:4200
# MongoDB:     http://localhost:4201
# MinIO:       http://localhost:4202
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
│   │   ├── download_workflow.py # Download, AI validate, hash
│   │   └── upload_workflow.py  # Scoped dedup & S3 upload
│   │
│   ├── activities/             # Temporal activity implementations
│   │   ├── ingest.py           # API-Football fetching (4 activities)
│   │   ├── monitor.py          # Event processing (10 activities)
│   │   ├── twitter.py          # Twitter search (6 activities)
│   │   ├── download.py         # Video download/validate (7 activities)
│   │   ├── upload.py           # S3 dedup/upload (12 activities)
│   │   └── rag.py              # Team alias RAG (3 activities)
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

**Schedule:** Every 30 seconds  
**Purpose:** Activate fixtures, detect events, trigger Twitter pipeline

```mermaid
flowchart TB
    TRIGGER[⏰ Every 30 seconds]
    
    ACTIVATE[Activate ready fixtures<br/>staging → active]
    FETCH[Fetch active fixtures<br/>from API-Football]
    STORE[Store in fixtures_live<br/>with _event_id]
    PROCESS[Process events<br/>Set operations]
    
    subgraph EVENTS["Event Handling"]
        NEW[NEW: Add with count=1]
        MATCH[MATCH: Increment count]
        REMOVE[REMOVED: Mark _removed]
    end
    
    TWITTER[🐦 Trigger TwitterWorkflow<br/>Fire-and-forget]
    SYNC[Sync fixture metadata<br/>Update API fields]
    COMPLETE[Complete if finished<br/>active → completed]
    
    TRIGGER --> ACTIVATE --> FETCH --> STORE --> PROCESS
    PROCESS --> NEW & MATCH & REMOVE
    MATCH -->|count=3| TWITTER
    PROCESS --> SYNC --> COMPLETE
```

### 3. RAGWorkflow (Pre-Caching Only)

**Trigger:** IngestWorkflow (daily) or manual  
**Purpose:** Pre-cache team aliases via Wikidata + LLM for faster Twitter searches

```mermaid
flowchart TB
    TRIGGER[Triggered by IngestWorkflow<br/>Pre-caching only]
    
    CACHE{Cache hit?}
    CACHED[Return cached aliases<br/>team_aliases collection]
    
    subgraph RAG["Full RAG Pipeline"]
        WIKI[Query Wikidata<br/>Search for team QID]
        FETCH[Fetch aliases<br/>English only]
        PREPROCESS[Preprocess to words<br/>Filter junk]
        LLM[llama.cpp LLM selects<br/>Best search terms]
    end
    
    SAVE[Save to cache<br/>team_aliases collection]
    
    TRIGGER --> CACHE
    CACHE -->|Yes| CACHED
    CACHE -->|No| WIKI --> FETCH --> PREPROCESS --> LLM --> SAVE
```

**Note**: RAGWorkflow is only used for pre-caching during IngestWorkflow. TwitterWorkflow resolves aliases at the start of each run using cached data.

**Alias Examples:**
- `"Liverpool"` → `["Liverpool", "LFC", "Reds", "Anfield"]`
- `"Atletico Madrid"` → `["Atletico", "Madrid", "ATM", "Atleti"]`
- `"Belgium"` (national) → `["Belgium", "Belgian", "Belgique"]`

### 4. TwitterWorkflow

**Trigger:** Per stable event (fire-and-forget from Monitor)  
**Purpose:** Resolve aliases, search Twitter with OR query, fire-and-forget downloads

```mermaid
flowchart TB
    TRIGGER[Triggered by Monitor<br/>Fire-and-forget]
    
    MONITOR_FLAG[Set _monitor_complete = true<br/>Proves we're running]
    ALIASES[Resolve team aliases<br/>From cache or RAG pipeline]
    
    subgraph LOOP["While download_count < 10 (max 15 attempts)"]
        CHECK[Check download count<br/>Exit if ≥ 10]
        VAR[Check event exists<br/>Abort if VAR'd]
        SEARCH["Search Twitter<br/>(Player OR Name) (Team OR Alias)"]
        DOWNLOAD[Start DownloadWorkflow<br/>Fire-and-forget (ABANDON)]
        SLEEP[workflow.sleep ~60s<br/>DURABLE TIMER]
    end
    
    DONE[_download_complete = true<br/>Set by UploadWorkflow at count=10]
    
    TRIGGER --> MONITOR_FLAG --> ALIASES --> CHECK
    CHECK -->|< 10| VAR --> SEARCH --> DOWNLOAD --> SLEEP
    SLEEP --> CHECK
    CHECK -->|≥ 10| DONE
```

**Key Points:**
- **Fire-and-forget downloads**: DownloadWorkflow registers itself at START using `$addToSet`
- **While loop with count check**: Continues until 10 DownloadWorkflows have registered
- **Durable timers**: `workflow.sleep(60)` survives worker restarts
- **UploadWorkflow sets `_download_complete`**: Only when it sees 10 registered downloads

### 5. DownloadWorkflow

**Trigger:** Per Twitter search attempt (fire-and-forget child of TwitterWorkflow)  
**Purpose:** Register, download, AI validate with clock extraction, hash, queue for upload

```mermaid
flowchart TB
    TRIGGER[Triggered by Twitter<br/>Fire-and-forget ABANDON]
    
    REG[Register in _download_workflows<br/>$addToSet — idempotent]
    
    subgraph DOWNLOAD["1. Download Phase (PARALLEL)"]
        DL[Download via syndication API]
        FILTER{Duration 3-60s?<br/>Aspect ratio ≥1.33?}
    end
    
    subgraph DEDUP1["2. MD5 Batch Dedup"]
        MD5[Remove exact duplicates<br/>within batch]
    end
    
    subgraph VALIDATE["3. AI Vision Validation (sequential)"]
        AI["Qwen3-VL-8B — 5 questions:<br/>SOCCER, SCREEN, CLOCK,<br/>ADDED, STOPPAGE_CLOCK"]
        TIMESTAMP["Timestamp validation<br/>±3 min vs API minute"]
        REJECT[Reject non-soccer<br/>or wrong match minute]
    end
    
    subgraph HASH["4. Hash Generation (PARALLEL)"]
        PHASH[Perceptual hash<br/>Dense 0.25s sampling<br/>Heartbeat every 5 frames]
    end
    
    UPLOAD[Signal UploadWorkflow<br/>via signal-with-start]
    
    TRIGGER --> REG --> DL
    DL --> FILTER
    FILTER -->|Pass| MD5
    MD5 --> AI --> TIMESTAMP
    TIMESTAMP -->|Verified or unverified| PHASH
    TIMESTAMP -->|Wrong minute| REJECT
    PHASH --> UPLOAD
```

**Shared Temp Volume**: Downloaded videos go to `/tmp/found-footy/{event_id}_{run_id}/`. This is a Docker volume shared across all workers, so any worker can process files downloaded by any other worker.

### 6. UploadWorkflow

**Trigger:** Via signal-with-start from DownloadWorkflow  
**Purpose:** Serialized S3 deduplication and upload per event

```mermaid
flowchart TB
    TRIGGER[Signal-with-start<br/>ID: upload-event_id<br/>SERIALIZED per event]
    
    QUEUE[Receive videos via signal<br/>Add to pending queue]
    
    subgraph BATCH["Process Each Batch"]
        FETCH[Fetch FRESH S3 state]
        MD5[MD5 dedup vs S3]
        SPLIT[Split by timestamp_verified]
        VPOOL[Verified pool<br/>vs verified S3]
        UPOOL[Unverified pool<br/>vs unverified S3]
    end
    
    subgraph UPLOAD["Upload Phase (PARALLEL)"]
        REPLACE[Replace inferior videos]
        NEW[Upload new videos]
    end
    
    SAVE[Save to MongoDB<br/>_s3_videos array]
    RANKS[Recalculate video ranks]
    CLEANUP[Cleanup uploaded files]
    COUNT[check_and_mark_download_complete<br/>Sets _download_complete at count=10]
    WAIT{More signals<br/>within 5 min?}
    DONE[Workflow complete]
    
    TRIGGER --> QUEUE --> FETCH --> MD5 --> SPLIT
    SPLIT --> VPOOL & UPOOL
    VPOOL & UPOOL --> REPLACE & NEW --> SAVE --> RANKS --> CLEANUP --> COUNT --> WAIT
    WAIT -->|Yes| QUEUE
    WAIT -->|No| DONE
```

**Signal-with-Start Pattern**: Multiple DownloadWorkflows can signal the same UploadWorkflow. Videos queue up and are processed in FIFO order. This prevents race conditions when parallel downloads complete simultaneously.

**Scoped Deduplication**: Perceptual hash comparison is split by `timestamp_verified`. Verified videos only compare against verified S3 videos, unverified against unverified. Both pools run in parallel via `asyncio.gather()`. This prevents a verified goal clip from being incorrectly replaced by an unverified clip from a different match moment.

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
  "_monitor_workflows": ["monitor-T15:45:00", "monitor-T15:45:30", "monitor-T15:46:00"],
  "_monitor_complete": true,
  "_download_workflows": ["download1-...", "download2-..."],
  "_download_complete": true,
  "_twitter_aliases": ["Liverpool", "LFC", "Reds"],
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
      "s3_url": "http://minio:9000/footy/...",
      "_s3_key": "123456/123456_40_306_Goal_1/abc123.mp4",
      "perceptual_hash": "dense:0.25:0.25=abc123,...",
      "width": 1920, "height": 1080,
      "file_size": 15000000,
      "duration": 45.2,
      "popularity": 3,
      "rank": 1,
      "timestamp_verified": true,
      "extracted_minute": 45,
      "timestamp_status": "verified"
    }
  ]
}
```

---

## 🐛 Debugging

### Check Workflow Status
```bash
# Temporal UI
open http://localhost:4200
```

### Check Event Data
```bash
# MongoDB (via Mongoku)
open http://localhost:4201
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
| Fixture stuck in active | Events missing `_download_complete` | Check worker logs for Twitter errors |
| Twitter search empty | Session expired | Re-login via VNC (port 4203) |
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
| Worker replicas | 2–8 | Auto-scaled by Scaler |
| Workflow tasks/worker | 10 | 20–80 total concurrent |
| Activities/worker | 30 | 60–240 total concurrent |
| API daily limit | 7,500 requests | Pro plan |
| Monitor interval | 30 seconds | Debounce polling |
| Debounce polls | 3 | ~90 seconds (3 × 30s) |
| Twitter searches | 10 per event | ~10 min window |
| Videos per search | 5 max | Up to 50 total |
| Duration filter | 3-60s | Strictly within range |
| Aspect ratio min | 1.33 (4:3) | Rejects vertical |
| Timestamp tolerance | ±3 minutes | AI vs API minute |
| LLM concurrency | 2 per worker | LLM_SEMAPHORE |
| Tracked teams | ~96 clubs + 15 national | Top-5 leagues dynamic |

---

## 📚 Additional Documentation

- **[ARCHITECTURE.md](./ARCHITECTURE.md)** — Collection schemas, video pipeline, activity reference
- **[ORCHESTRATION.md](./ORCHESTRATION.md)** — Event lifecycle, state machine, debouncing
- **[TEMPORAL_WORKFLOWS.md](./TEMPORAL_WORKFLOWS.md)** — Activity specifications, retry policies, heartbeats
- **[TWITTER_AUTH.md](./TWITTER_AUTH.md)** — Browser automation, cookie management
- **[RAG_IMPLEMENTATION.md](./RAG_IMPLEMENTATION.md)** — Wikidata + LLM alias pipeline

---

## 📜 License

MIT
