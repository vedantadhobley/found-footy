# Temporal Workflow Tests

Organized testing structure for found-footy Temporal workflows.

## Directory Structure

```
tests/
├── fixtures.py              # Test data and MongoDB setup/teardown utilities
└── workflows/               # Workflow integration tests
    ├── test_ingest.py      # Test daily fixture ingestion
    ├── test_monitor.py     # Test monitor → event chain
    └── test_e2e.py         # Full pipeline test
```

## Running Tests

All tests should be run **inside the worker container** where Temporal client and dependencies are available:

```bash
# Test ingest workflow (fetches today's real fixtures)
docker exec -it found-footy-worker python /workspace/tests/workflows/test_ingest.py

# Test monitor workflow (uses test fixture with incomplete goal)
docker exec -it found-footy-worker python /workspace/tests/workflows/test_monitor.py

# Test full end-to-end pipeline
docker exec -it found-footy-worker python /workspace/tests/workflows/test_e2e.py
```

## Test Fixtures

The `tests/fixtures.py` module provides:

### Test Data Functions
- `get_test_fixture_with_goal()` - Liverpool vs Man Utd with 1 goal (M. Salah 23')
- `get_test_fixture_no_goals()` - Same match but 0-0
- `get_test_fixture_finished()` - Finished match (FT status)

### Setup Functions
- `setup_test_fixture_in_staging()` - Insert fixture into staging
- `setup_test_fixture_in_active()` - Insert into active with empty events
- `setup_test_fixture_in_active_with_incomplete_event()` - Insert with incomplete goal event (perfect for testing debounce)

### Cleanup
- `cleanup_test_fixtures()` - Remove all test fixtures (ID 999999) from all collections

## Test Scenarios

### 1. Ingest Test (`test_ingest.py`)
**What it tests:** Daily fixture ingestion from API
**Setup:** None (uses real API data)
**Expected:**
- Fetches today's fixtures
- Filters to 50 tracked teams
- Categorizes by status (staging/active/completed)
- Stores in MongoDB

### 2. Monitor Test (`test_monitor.py`)
**What it tests:** Monitor workflow detects changes and triggers EventWorkflow
**Setup:** 
- Inserts test fixture (ID 999999) into `fixtures_active`
- Fixture has 1 incomplete goal event
**Expected:**
- Monitor fetches fixture from API
- Stores in `fixtures_live`
- Compares with `fixtures_active`
- Detects incomplete event
- Triggers `EventWorkflow` as child

### 3. End-to-End Test (`test_e2e.py`)
**What it tests:** Full pipeline chain
**Setup:** Same as monitor test
**Expected:**
1. Monitor runs
2. Event workflow triggered (debounces events)
3. Twitter workflow triggered (searches for videos)
4. Download workflow triggered (downloads videos)

## Verification

After running tests, verify results:

### Temporal UI
```
http://localhost:4100
```
- Check workflow execution history
- Verify child workflows were triggered
- Check activity logs

### MongoDB
```bash
# Count fixtures in each collection
docker exec found-footy-mongo mongosh -u ffuser -p ffpass \
  --authenticationDatabase admin found_footy \
  --eval "db.fixtures_staging.countDocuments({})"

# View test fixture in active collection
docker exec found-footy-mongo mongosh -u ffuser -p ffpass \
  --authenticationDatabase admin found_footy \
  --eval "db.fixtures_active.findOne({_id: 999999})"
```

### Worker Logs
```bash
docker logs found-footy-worker --tail 50
```

## Test Fixture Details

**Test Fixture ID:** `999999`
**Match:** Liverpool vs Manchester United  
**Status:** 1H (First Half, 23')  
**Score:** 1-0 (Salah 23')  
**Purpose:** Simulates a live match with incomplete goal data

The incomplete goal event is perfect for testing debounce logic because:
- Player/assist IDs are null initially
- API-Football fills in details gradually
- Monitor detects changes and triggers EventWorkflow
- EventWorkflow waits for 3 stable checks before proceeding

## Cleanup

Tests automatically clean up after themselves. If interrupted:

```python
from tests.fixtures import cleanup_test_fixtures
cleanup_test_fixtures()
```

Or manually:
```bash
docker exec found-footy-mongo mongosh -u ffuser -p ffpass \
  --authenticationDatabase admin found_footy \
  --eval "db.fixtures_staging.deleteMany({_id: 999999}); \
          db.fixtures_active.deleteMany({_id: 999999}); \
          db.fixtures_live.deleteMany({_id: 999999}); \
          db.fixtures_completed.deleteMany({_id: 999999});"
```
