# Found Footy - Dagster Migration Complete! 🚀

Football highlights automation migrated from Prefect to Dagster orchestration.

## 📊 Architecture Overview

```
found-footy/
├── src/                          # New Dagster codebase (clean separation)
│   ├── __init__.py              # Main Definitions object
│   ├── api/                     # API clients (API-Football)
│   ├── data/                    # Storage classes (MongoDB, S3)
│   ├── utils/                   # Pure business logic (NO orchestration)
│   ├── assets/                  # Dagster assets (7 total)
│   │   ├── fixtures/            # Ingest, monitor, advance
│   │   ├── goals/               # Process goals
│   │   ├── twitter/             # Scrape videos
│   │   └── videos/              # Download & filter
│   ├── jobs/                    # Asset jobs (4 pipelines)
│   ├── schedules/               # Cron schedules (2 schedules)
│   └── resources/               # MongoDB, S3, Twitter resources
│
├── found_footy/                 # Original Prefect code (KEEP FOR NOW)
│   └── ...                      # Will be deleted after validation
│
├── workspace.yaml               # Dagster workspace config
├── dagster.yaml                 # Dagster instance config (PostgreSQL)
├── docker-compose.dagster.yml   # New Dagster stack
└── docker-compose.yml           # Original Prefect stack
```

## 🎯 What Changed

### ✅ Before (Prefect)
- Prefect flows with `@flow` and `@task` decorators
- Prefect deployments and work pools
- Prefect variables for configuration
- Complex flow triggering via `run_deployment()`

### ✅ After (Dagster)
- Dagster assets with `@asset` decorators
- Asset jobs for grouping
- ConfigurableResource for dependencies
- Clean separation of business logic

## 🏗️ Key Components

### Assets (7 total)

**Fixtures Group:**
- `ingest_fixtures` - Fetch and categorize fixtures daily
- `monitor_fixtures` - Monitor active fixtures every 3 minutes
- `advance_fixtures` - Move fixtures between collections

**Goals Group:**
- `process_goals` - Handle discovered goal events

**Twitter Group:**
- `scrape_twitter_videos` - Find goal videos on Twitter

**Videos Group:**
- `download_videos` - Download videos with yt-dlp
- `filter_videos` - Deduplicate using OpenCV

### Jobs (4 pipelines)

1. `fixtures_pipeline` - Full fixtures workflow
2. `goal_processing` - Goal event handling
3. `twitter_scraping` - Twitter video search
4. `video_pipeline` - Download & deduplication

### Schedules (2 automated)

1. `daily_ingest_schedule` - Midnight UTC (0 0 * * *)
2. `monitor_schedule` - Every 3 minutes (*/3 * * * *)

### Resources (3 services)

1. `mongo` - MongoDB for application data
2. `s3` - MinIO for video storage
3. `twitter_session` - Twitter scraping service

## 🚀 Getting Started

### Prerequisites
- Docker & Docker Compose
- API-Football API key (RapidAPI)
- Twitter account (for scraping service)

### Environment Setup

Create `.env` file:
```bash
# API Keys
RAPIDAPI_KEY=your_rapidapi_key_here
API_FOOTBALL_KEY=your_api_football_key_here

# MongoDB
MONGODB_URI=mongodb://founduser:footypass@mongo:27017/found_footy?authSource=admin

# S3/MinIO
S3_ENDPOINT_URL=http://minio:9000
S3_ACCESS_KEY=founduser
S3_SECRET_KEY=footypass
S3_BUCKET_NAME=footy-videos

# Twitter Session Service
TWITTER_SESSION_URL=http://twitter-session:8888
```

### Start Dagster Stack

```bash
# Build and start all services
docker-compose -f docker-compose.dagster.yml up -d

# Check logs
docker-compose -f docker-compose.dagster.yml logs -f dagster-webserver

# Access Dagster UI
open http://localhost:3000
```

### Services & Ports

| Service | Port | Description |
|---------|------|-------------|
| Dagster UI | 3000 | Web interface for managing pipelines |
| Mongo Express | 8081 | MongoDB admin UI |
| MinIO Console | 9001 | S3-compatible storage UI |
| MinIO API | 9000 | S3 API endpoint |
| Twitter Session | 8888 | Twitter scraping service |

## 📋 Testing the Migration

### 1. Verify Assets Load

```bash
# Check Dagster logs for any import errors
docker logs found-footy-dagster-webserver

# Should see:
# ✅ Loaded 7 assets
# ✅ Loaded 4 jobs
# ✅ Loaded 2 schedules
```

### 2. Test Manual Asset Execution

1. Open Dagster UI: http://localhost:3000
2. Go to **Assets** tab
3. Click `ingest_fixtures`
4. Click **Materialize**
5. Check execution logs

### 3. Enable Schedules

1. Go to **Automation** tab
2. Enable `daily_ingest_schedule`
3. Enable `monitor_schedule`
4. Verify schedules appear in daemon logs

### 4. Monitor Active Fixtures

```bash
# Watch the monitor schedule run every 3 minutes
docker logs -f found-footy-dagster-daemon

# Should see:
# 🔍 Monitored active fixtures...
# ⚽ Processing fixture XXX - N new goals
```

## 🔧 Development Workflow

### Local Development (without Docker)

```bash
# Install dependencies
pip install -r requirements.txt

# Set environment variables
export MONGODB_URI="mongodb://localhost:27017/found_footy"
export RAPIDAPI_KEY="your_key"

# Start Dagster dev server
dagster dev -f src/__init__.py

# Open UI
open http://localhost:3000
```

### Making Changes

1. **Edit assets:** Modify files in `src/assets/`
2. **Add business logic:** Add functions to `src/utils/`
3. **Update resources:** Modify `src/resources/__init__.py`
4. **Reload Dagster:** Changes auto-reload in dev mode

### Running Tests

```bash
# Run pytest
docker-compose -f docker-compose.dagster.yml exec dagster-webserver pytest

# Or locally
pytest tests/
```

## 🎓 Dagster Concepts

### Assets vs. Flows

**Prefect Flows:**
```python
@flow
def ingest_flow(date_str: str = None):
    params = process_parameters_task(date_str)
    fixtures = fetch_fixtures_task(params)
    # ...
```

**Dagster Assets:**
```python
@asset
def ingest_fixtures_asset(context, date_str: Optional[str] = None):
    params = fixture_logic.process_parameters(date_str)
    fixtures = fixture_logic.fetch_fixtures(params)
    # Returns Output with metadata
```

### Key Differences

| Concept | Prefect | Dagster |
|---------|---------|---------|
| Orchestration Unit | Flow | Asset |
| Task | `@task` | Pure Python function |
| Scheduling | Deployment | ScheduleDefinition |
| Dependencies | Prefect server | ConfigurableResource |
| UI | Prefect Cloud/Server | Dagster webserver |
| Storage | Blocks | Resources |

## 📈 Migration Benefits

✅ **Clean Separation** - Business logic in `utils/`, orchestration in `assets/`
✅ **Better Testing** - Pure functions easier to test
✅ **Type Safety** - Dagster's strong typing catches errors early
✅ **Asset Lineage** - Visual dependency graph
✅ **Partition Support** - Time-based and custom partitions
✅ **Better Observability** - Rich metadata and logging

## 🐛 Troubleshooting

### Assets not loading?

```bash
# Check workspace config
cat workspace.yaml

# Verify Python path
docker exec found-footy-dagster-webserver python -c "from src import defs; print(len(defs.assets))"
```

### Import errors?

```bash
# Check Python path in container
docker exec found-footy-dagster-webserver env | grep PYTHONPATH

# Should see: PYTHONPATH=/app
```

### MongoDB connection issues?

```bash
# Test MongoDB connection
docker exec found-footy-mongo mongosh -u founduser -p footypass --eval "db.adminCommand('ping')"
```

### Schedules not running?

```bash
# Check daemon status
docker logs found-footy-dagster-daemon

# Verify schedules enabled
# Go to Automation tab in UI
```

## 📚 Next Steps

1. **Validate Migration** - Run both Prefect and Dagster in parallel
2. **Complete TODOs** - Implement full download/filter logic
3. **Add Sensors** - Dynamic triggering for advance/twitter flows
4. **Add Tests** - Unit tests for business logic
5. **Performance Tuning** - Optimize batch API calls
6. **Delete Prefect** - Once fully validated, remove `found_footy/`

## 🔗 Useful Links

- [Dagster Docs](https://docs.dagster.io)
- [Dagster University](https://dagster.io/university)
- [Asset Tutorial](https://docs.dagster.io/tutorial)
- [Dagster Slack](https://dagster.io/slack)

## 📝 Notes

- **Prefect code preserved** in `found_footy/` directory
- **Keep both running** until fully validated
- **TODOs exist** in download/filter assets for full implementation
- **Sensors needed** for dynamic scheduling (advance/twitter flows)

---

**Migration completed:** November 13, 2025  
**Total assets:** 7  
**Total lines of code:** ~2450  
**Zero Prefect dependencies in src/** ✅
