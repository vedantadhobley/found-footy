# Found Footy - Temporal Implementation

## Structure

```
src/
├── workflows/              # Temporal workflows (orchestration)
│   ├── ingest_workflow.py      # Daily at 00:05 UTC - fetch and categorize fixtures
│   ├── monitor_workflow.py     # Every 30 seconds - track fixtures, debounce events inline
│   ├── rag_workflow.py         # Pre-caching only - resolve team aliases via Wikidata + LLM
│   ├── twitter_workflow.py     # Per event - resolve aliases, search videos 10×
│   ├── download_workflow.py    # Per event - download, validate, hash, MD5 dedup
│   └── upload_workflow.py      # Per event - serialized S3 dedup and upload
├── activities/             # Temporal activities (actual work)
│   ├── ingest.py          # Fetch and categorize fixtures (2 activities)
│   ├── monitor.py         # Activate, fetch, compare, process_fixture_events (9 activities)
│   ├── rag.py             # Team alias RAG (Wikidata + LLM) (4 activities)
│   ├── twitter.py         # Check, get_data, search, save, update (5 activities)
│   ├── download.py        # Download, validate, hash, cleanup, count (5 activities)
│   └── upload.py          # S3 dedup, upload, save, cleanup (9 activities)
├── data/                  # Data layer
│   ├── mongo_store.py     # MongoDB operations (5-collection architecture)
│   └── s3_store.py        # MinIO S3 operations
├── utils/                 # Utilities
│   ├── event_config.py    # Event filtering (Goals only)
│   └── team_data.py       # Dynamic top-5 league teams (96+) + national teams
└── worker.py              # Temporal worker (runs workflows & activities)
```

## Workflow Chain

```
IngestWorkflow (daily 00:05 UTC)
  ├─> Fetch 3 days of fixtures (today+tomorrow+day_after)
  ├─> Skip fixtures that already exist (duplicate detection)
  ├─> Pre-cache team aliases via RAGWorkflow (both teams)
  ↓
MonitorWorkflow (every 30 seconds)
  ├─> process_fixture_events (inline debounce)
  └─> TwitterWorkflow (per stable event, fire-and-forget)
        ├─> Resolves aliases at start (from cache)
        ├─> 10 search attempts with ~3min sleep between
        ↓ triggers for each batch of videos found
      DownloadWorkflow (BLOCKING per batch)
        ├─> Download, validate (AI), hash, MD5 dedup
        ↓ delegates to
      UploadWorkflow (BLOCKING, serialized per event)
        ├─> Deterministic ID: upload-{event_id}
        └─> S3 dedup, upload, save to MongoDB
```

## Key Features

- **Set-based debounce**: Event ID includes player_id, no hash comparison needed
- **Inline processing**: MonitorWorkflow processes events directly (no separate EventWorkflow)
- **Serialized S3 ops**: UploadWorkflow with deterministic ID prevents race conditions
- **Per-video retry**: Download activities have individual retries
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

- ✅ IngestWorkflow - Fetches and categorizes fixtures, pre-caches team aliases
- ✅ MonitorWorkflow - Polls fixtures, debounces events inline
- ✅ RAGWorkflow - Wikidata RAG + LLM for team aliases (pre-caching only)
- ✅ TwitterWorkflow - Resolves aliases, searches Twitter 10× via browser automation
- ✅ DownloadWorkflow - Downloads, validates, hashes, MD5 deduplicates
- ✅ UploadWorkflow - Serialized S3 deduplication and upload per event
- ✅ MongoDB 5-collection architecture
- ✅ Set-based debounce (no hash comparison)
