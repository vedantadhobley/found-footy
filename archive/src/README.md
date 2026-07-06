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
│   ├── ingest.py          # Fetch and categorize fixtures (4 activities)
│   ├── monitor.py         # Activate, fetch, compare, process events (10 activities)
│   ├── rag.py             # Team alias RAG — Wikidata + LLM (3 activities)
│   ├── twitter.py         # Search, save, monitor_complete, count (6 activities)
│   ├── download.py        # Download, AI validate, hash, register (7 activities)
│   └── upload.py          # Scoped dedup, S3 upload, rank, cleanup (12 activities)
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
      DownloadWorkflow (fire-and-forget per attempt)
        ├─> Register in _download_workflows ($addToSet)
        ├─> Download, AI validate (clock extraction), hash, MD5 dedup
        ↓ signals via signal-with-start
      UploadWorkflow (serialized per event, signal-based FIFO)
        ├─> Deterministic ID: upload-{event_id}
        ├─> Scoped dedup (verified vs unverified pools, asyncio.gather)
        └─> S3 upload, rank (verified → popularity → file_size)
```

## Key Features

- **Set-based debounce**: Event ID includes player_id, no hash comparison needed
- **Inline processing**: MonitorWorkflow processes events directly (no separate EventWorkflow)
- **Serialized S3 ops**: UploadWorkflow with deterministic ID prevents race conditions
- **Scoped dedup**: Verified and unverified videos deduped in separate pools
- **AI clock extraction**: Qwen3-VL-8B extracts broadcast clock, validates ±3 min vs API
- **Per-video retry**: Download activities have individual retries
- **Auto-scaling**: Scaler manages 2–8 worker/Twitter instances
- **Firefox automation**: Twitter search via browser with saved profile

## Quick Start

```bash
# Start services
docker compose -f docker-compose.dev.yml up -d

# View worker logs
docker compose -f docker-compose.dev.yml logs -f worker

# Temporal UI
open http://localhost:4200

# MongoDB Express
open http://localhost:4201
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
