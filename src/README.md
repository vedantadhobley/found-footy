# Found Footy - Temporal Implementation

## Structure

```
src/
├── workflows/          # Temporal workflows (orchestration)
│   ├── ingest_workflow.py      # Daily at 00:05 UTC - fetch and categorize fixtures
│   ├── monitor_workflow.py     # Every minute - track active fixtures
│   ├── event_workflow.py       # Per fixture - debounce events
│   ├── twitter_workflow.py     # Per event - search videos
│   └── download_workflow.py    # Per event - download & upload
├── activities/         # Temporal activities (actual work)
│   ├── ingest.py      # Fetch and categorize fixtures
│   ├── monitor.py     # Activate, fetch, compare
│   ├── event.py       # Debounce logic
│   ├── twitter.py     # Twitter search
│   └── download.py    # Download, dedupe, S3 upload
├── data/              # Data layer
│   └── mongo_store.py # MongoDB operations (4-collection architecture)
└── worker.py          # Temporal worker (runs workflows & activities)
```

## Workflow Chain

```
IngestWorkflow (daily)
  ↓
MonitorWorkflow (every minute)
  ↓ triggers per fixture with changes
EventWorkflow
  ↓ triggers per stable event
TwitterWorkflow
  ↓ triggers if videos found
DownloadWorkflow
```

## Key Differences from Dagster

- **Event-driven**: Workflows trigger child workflows immediately (no polling/sensors)
- **Durable**: Temporal automatically persists state, handles retries
- **UI visibility**: Each workflow execution shows as separate run in Temporal UI
- **Clean code**: No op decorators, context passing, or instance management

## Next Steps

1. Install dependencies: `pip install -r requirements.txt`
2. Start services: `docker compose -f docker-compose.dev.yml up -d`
3. Temporal UI: `localhost:4105`
4. Migrate activity logic from `src-dagster/` (TODOs marked in activity files)
5. Test with manual workflow trigger

## Migration Status

- ✅ Structure created
- ✅ Workflow orchestration defined
- ✅ Activity stubs created
- ⏳ Activity implementations (copy from src-dagster)
- ⏳ API client (api-football.com)
- ⏳ Twitter integration (use existing twitter/ directory)
- ⏳ S3/MinIO client
- ⏳ Testing scripts
