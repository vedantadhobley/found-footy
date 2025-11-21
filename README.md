# Found Footy - Dagster Migration 🚀⚽

Automated football highlights pipeline - Migrated from Prefect to Dagster for better orchestration, cost efficiency, and cleaner architecture.

---

## 🎯 Current Status

- ✅ Architecture Complete - One pipeline per goal, clean parameter flow
- ✅ Cost Optimized - 5min monitoring = 40% fewer API calls
- ✅ Pipeline Verified - Loads successfully in Dagster
- ⚠️ Needs Testing - Goal confirmation + Twitter scraping

---

## 🔌 Port Configuration

**Port Range:** 3100-3199 (Found-footy allocation)

**Development Access (via SSH forwarding):**
- **Dagster UI:** http://localhost:3100
- **MongoDB Express:** http://localhost:3101
- **MinIO Console:** http://localhost:3102
- **Twitter Login:** http://localhost:3103

**Internal Services (no external access):**
- PostgreSQL: `postgres:5432`
- MongoDB: `mongo:27017`
- MinIO API: `minio:9000`

> See [Multi-Project Setup Guide](../MULTI_PROJECT_SETUP.md) for full port allocation details.

---

## 📊 System Architecture

```
COST-OPTIMIZED MONITORING (Every 5 minutes)
┌────────────────────────────────────────────┐
│ monitor_fixtures_job (scheduled)           │
│ - Only queries API if fixtures_active ≠ ∅  │
│ - Detects goal changes                     │
│ - Updates fixtures.goals[] in MongoDB      │
└────────────────┬───────────────────────────┘
                 │
                 ▼
SENSOR POLLING (Every 60 seconds)
┌────────────────────────────────────────────┐
│ goal_pipeline_trigger_sensor               │
│ - Finds new goals in fixtures collection   │
│ - Passes: fixture_id, minute, player_name  │
│ - Triggers ONE pipeline per goal           │
└────────────────┬───────────────────────────┘
                 │
                 ▼
ONE PIPELINE PER GOAL (Parallel)
┌────────────────────────────────────────────┐
│ 1. process_goal_op                         │
│    - Reads full data from MongoDB          │
│    - Creates goal in goals collection      │
│    - Returns: goal_id + metadata           │
├────────────────────────────────────────────┤
│ 2. scrape_twitter_op (retry: 3x)           │
│    - Searches Twitter for videos           │
│    - Stores video URLs in MongoDB          │
│    - Returns: video_ids[]                  │
├────────────────────────────────────────────┤
│ 3. download_videos_op (retry: 3x)          │
│    - Downloads videos via yt-dlp           │
│    - Stores in temp directory              │
│    - Returns: video_paths[]                │
├────────────────────────────────────────────┤
│ 4. upload_videos_op (retry: 3x)            │
│    - Uploads to S3/MinIO                   │
│    - Cleans up temp files                  │
│    - Returns: uploaded_count               │
├────────────────────────────────────────────┤
│ 5. filter_videos_op                        │
│    - Deduplicates (hash + OpenCV)          │
│    - Deletes duplicates from S3            │
│    - Marks goal as completed               │
└────────────────────────────────────────────┘
```

---

## 🏗️ Project Structure

```
found-footy/
├── src/                          # Dagster codebase (main orchestration)
│   ├── jobs/                     # 3 main jobs
│   │   ├── ingest_fixtures.py    # Daily fixture ingestion
│   │   ├── monitor.py            # Goal detection (5min)
│   │   └── goal_pipeline.py      # 5-op pipeline per goal
│   ├── sensors/                  # Event triggers
│   │   └── goal_pipeline_trigger.py
│   ├── schedules/                # Time triggers
│   │   └── __init__.py           # daily_ingest, monitor
│   ├── api/                      # External APIs
│   ├── data/                     # Storage (MongoDB, S3)
│   └── utils/                    # Business logic
├── twitter/                      # 🐦 Independent Twitter scraper service
│   ├── app.py                    # FastAPI REST API
│   ├── session.py                # Browser session manager
│   ├── auth.py                   # Authentication logic
│   ├── config.py                 # Configuration
│   ├── Dockerfile                # Container image
│   ├── requirements.txt          # Dependencies
│   └── README.md                 # Full documentation
├── found_footy/                  # Original Prefect code (legacy)
├── docker-compose.yml            # Production stack
├── docker-compose.dev.yml        # Development stack
├── workspace.yaml                # Dagster config
└── README_PREFECT.md             # Prefect docs (historical)
```

---

## 🚀 Quick Start

### 1. Setup Twitter (One-Time)

The Twitter service will try to authenticate automatically using credentials from `.env`.

If that fails (2FA/CAPTCHA), use the manual login UI:

```bash
# Start services
docker compose up -d

# Open login UI in browser
open http://localhost:3103/login

# Follow instructions to copy 3 cookies from DevTools
```

See detailed guide: [`twitter/QUICKSTART.md`](twitter/QUICKSTART.md)

### 2. Start All Services

```bash
docker compose up -d
```

### 3. Access Dagster UI

```bash
open http://localhost:3000
```

### 4. Enable Automation

Go to **Automation** in Dagster UI:

**Schedules:**
- Enable `monitor_schedule` (every 5 min)
- Enable `daily_ingest_schedule` (midnight UTC)

**Sensors:**
- Enable `goal_pipeline_trigger` (checks every 60s)

---

## 💡 Key Design Decisions

### ✅ MongoDB as Source of Truth

- Sensor passes **minimal identifiers** (fixture_id, minute, player_name)
- Ops **read full goal data** from MongoDB fixtures collection
- No large JSON in run_config

### ✅ One Pipeline Per Goal

- 3 goals detected = 3 parallel pipeline runs
- Failures isolated per goal
- Better observability

### ✅ Cost Optimization

- Monitor frequency: 5 minutes (was 3 minutes)
- **40% fewer API calls** (20/hour → 12/hour)
- **Estimated savings**: $20/month → $12/month

### ✅ Retry Policies

All external service ops have exponential backoff:
- scrape_twitter: 3x retries, 10s delay
- download_videos: 3x retries, 15s delay
- upload_videos: 3x retries, 10s delay

### ✅ Download/Upload Separation

- Download from Twitter → separate op
- Upload to S3 → separate op
- Independent failure handling

---

## 📋 TODO: Next Steps

### 🔥 PRIORITY 1: Goal Confirmation Strategy (BRILLIANT!)

**Problem**: Goals might be incorrect/incomplete when first detected

**Solution**: 2-Cycle Confirmation

```
Cycle 1 (00:00): Monitor detects goal at 67' - Messi
  → Store in pending_goals collection
  → Don't trigger pipeline yet

Cycle 2 (05:00): Monitor detects SAME goal at 67' - Messi
  → Check pending_goals from last cycle
  → If exists → CONFIRMED! → Move to fixtures.goals[]
  → If missing → CANCELLED/VAR → Discard

Cycle 3 (10:00): Sensor picks up confirmed goal
  → Trigger pipeline with stable data
```

**Benefits:**
- No extra API calls (reuse monitor cycle)
- 5-10 min delay stabilizes API data
- Prevents false positives
- Higher data quality

**Implementation:**
1. Add `pending_goals` MongoDB collection
2. Update `monitor.py` to check pending vs current
3. Only process confirmed goals

---

### 🔧 PRIORITY 2: Fix Twitter Scraping ✅ SOLUTION READY

**Current Issue**: Twitter scraping broken (Selenium login automation fails)

**✅ SOLUTION IMPLEMENTED: Cookie-Based Auth in Docker**
- One-time manual login via `./scripts/setup_twitter_docker.sh`
- Cookies saved to Docker volume (persistent)
- Service reuses cookies automatically (~30 day lifespan)
- Chrome already installed in Docker image
- Works identically on WSL, Mini PC, any Docker host

---

### 🎯 PRIORITY 3: Testing & Polish

**Twitter Scraping Test (After Mini PC Setup)**:
```bash
# One-time setup on Mini PC (manual login, saves cookies)
./scripts/setup_twitter_docker.sh

# Then start everything
docker compose up -d

# Test Twitter service
curl -X POST http://localhost:8888/search \
  -H "Content-Type: application/json" \
  -d '{"search_query":"Ronaldo goal","max_results":3}'
```

**Full Pipeline Test (After Mini PC)**:
- Test full pipeline with live data
- Validate goal confirmation works
- Test Twitter scraping on Mini PC
- Verify S3 storage
- Test OpenCV deduplication
- Add monitoring alerts
- Document Mini PC deployment

---

## 🧪 Testing

### Verify Services

```bash
docker-compose -f docker-compose.dagster.yml ps
```

### Check Dagster Loads

```bash
docker logs found-footy-dagster-webserver
# Should see: ✅ Loaded 3 jobs, 2 schedules, 1 sensor
```

### Test Manual Pipeline

1. Go to **Jobs** → **goal_pipeline**
2. Click **Launch Run**
3. Provide config:

```json
{
  "ops": {
    "process_goal": {
      "config": {
        "fixture_id": "12345",
        "goal_minute": 67,
        "player_name": "Lionel Messi",
        "mongo_uri": "mongodb://localhost:27017",
        "db_name": "found_footy"
      }
    }
  }
}
```

---

## 📊 Prefect vs Dagster Comparison

| Aspect | Prefect | Dagster |
|--------|---------|---------|
| Orchestration | Flows + Tasks | Jobs + Ops |
| Scheduling | Deployments | ScheduleDefinition |
| Event Triggers | run_deployment() | Sensors |
| Config | Function params | Config classes |
| Monitoring | 3 minutes | 5 minutes |
| Cost | ~$20/month | ~$12/month |

---

## 🐛 Troubleshooting

### Pipeline Not Loading?

```bash
docker logs found-footy-dagster-webserver | grep -i error
```

### MongoDB Issues?

```bash
docker exec found-footy-mongo mongosh -u founduser -p footypass --eval "db.adminCommand('ping')"
```

### Sensor Not Triggering?

- Check if enabled in Dagster UI
- Verify MongoDB has goals
- Check sensor logs in UI

---

## 🔗 Services

| Service | Port | URL |
|---------|------|-----|
| Dagster UI | 3000 | http://localhost:3000 |
| MongoDB | 27017 | - |
| Mongo Express | 8081 | http://localhost:8081 |
| MinIO Console | 9001 | http://localhost:9001 |
| MinIO API | 9000 | - |
| Twitter Session | 8888 | http://localhost:8888 |

---

## 📚 Documentation

- **README.md** - This file (system overview)
- **README_PREFECT.md** - Original Prefect docs
- **.archive/** - Old migration docs

---

## ✅ What's Working

- Clean job architecture (one pipeline per goal)
- MongoDB as source of truth
- 40% cost reduction (5min monitoring)
- Retry policies on external services
- Pipeline loads successfully
- Cookie-based Twitter auth (Docker-ready)
- All services containerized

---

## 🚧 What Needs Work

- Goal confirmation strategy (2-cycle validation)
- Twitter setup on Mini PC (run setup script once)
- End-to-end testing (not tested live)
- Mini PC deployment (use same Docker setup)

---

**Last Updated**: November 14, 2025  
**Status**: Architecture Complete, Ready for Testing  
**Next Session**: Goal confirmation + Twitter scraping
