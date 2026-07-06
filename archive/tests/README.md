# Temporal Workflow Tests

Integration tests for found-footy Temporal workflows.

## Running Tests

Tests run inside the worker container where Temporal client is available:

```bash
# Test IngestWorkflow (fetches today's real fixtures)
docker exec -it found-footy-worker python /workspace/tests/workflows/test_ingest.py

# Test full pipeline (inserts test fixture, watches MonitorWorkflow process it)
docker exec -it found-footy-worker python /workspace/tests/workflows/test_pipeline.py
```

## Test Files

### `test_ingest.py`
Tests the daily fixture ingestion workflow:
- Connects to Temporal
- Runs IngestWorkflow
- Verifies fixtures stored in MongoDB

### `test_pipeline.py`
End-to-end pipeline test:
- Fetches a real fixture from API
- Resets it to staging state (NS, no events)
- Inserts into fixtures_staging
- MonitorWorkflow automatically picks it up and:
  1. Activates it (staging → active)
  2. Fetches fresh data (with events)
  3. Debounces events (3 cycles)
  4. Triggers TwitterWorkflow
  5. Downloads and uploads to S3

## Monitoring Test Progress

```bash
# Temporal UI - watch workflows
http://localhost:4200

# Twitter VNC - see Firefox browser
http://localhost:4203

# MongoDB Express - check collections
http://localhost:4201

# Worker logs
docker logs -f found-footy-worker
```

## Test Fixture

Default test fixture: **Liverpool vs Arsenal (ID: 1378993)**

The test:
1. Fetches this fixture from API
2. Resets to NS (Not Started) status
3. Clears events, nulls scores
4. Inserts into staging
5. MonitorWorkflow takes over

## Cleanup

Test fixtures are cleaned automatically. If needed manually:

```bash
docker exec found-footy-mongo mongosh -u ffuser -p ffpass \
  --authenticationDatabase admin found_footy \
  --eval "db.fixtures_staging.deleteMany({_id: 1378993}); \
          db.fixtures_active.deleteMany({_id: 1378993}); \
          db.fixtures_live.deleteMany({_id: 1378993}); \
          db.fixtures_completed.deleteMany({_id: 1378993});"
```
