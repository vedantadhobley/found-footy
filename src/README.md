# Found Footy - Temporal Implementation

## Structure

```
src/
├── workflows/              # Temporal workflows (orchestration)
│   ├── ingest_workflow.py      # Daily at 00:05 UTC - fetch and categorize fixtures
│   ├── monitor_workflow.py     # Every minute - track fixtures, debounce events inline
│   ├── twitter_workflow.py     # Per event - search videos via browser automation
│   └── download_workflow.py    # Per event - download & upload with per-video retry
├── activities/             # Temporal activities (actual work)
│   ├── ingest.py          # Fetch and categorize fixtures
│   ├── monitor.py         # Activate, fetch, compare, process_fixture_events
│   ├── twitter.py         # 3 activities: get_data, search, save
│   └── download.py        # 5 activities for download/upload
├── data/                  # Data layer
│   ├── mongo_store.py     # MongoDB operations (4-collection architecture)
│   └── s3_store.py        # MinIO S3 operations
├── utils/                 # Utilities
│   ├── event_config.py    # Event filtering (Goals only)
│   └── team_data.py       # 50 tracked teams
└── worker.py              # Temporal worker (runs workflows & activities)
```

## Workflow Chain

```
IngestWorkflow (daily 00:05 UTC)
  ↓
MonitorWorkflow (every minute)
  ├─> process_fixture_events (inline debounce)
  └─> TwitterWorkflow (per stable event)
        ↓ triggers if videos found
      DownloadWorkflow (per event)
```

## Key Features

- **Set-based debounce**: Event ID includes player_id, no hash comparison needed
- **Inline processing**: MonitorWorkflow processes events directly (no separate EventWorkflow)
- **Per-video retry**: DownloadWorkflow has 5 granular activities with individual retries
- **Firefox automation**: Twitter search via browser with saved profile

## Quick Start

```bash
# Start services
docker compose -f docker-compose.dev.yml up -d

# View worker logs
docker compose -f docker-compose.dev.yml logs -f worker

# Temporal UI
open http://localhost:4100

# MongoDB Express
open http://localhost:4101
```

## Implementation Status

- ✅ IngestWorkflow - Fetches and categorizes fixtures
- ✅ MonitorWorkflow - Polls fixtures, debounces events inline
- ✅ TwitterWorkflow - Searches Twitter via browser automation
- ✅ DownloadWorkflow - Downloads, deduplicates, uploads to S3
- ✅ MongoDB 4-collection architecture
- ✅ Set-based debounce (no hash comparison)
- ✅ Per-video retry policies
