# Job Execution Guide

## Current Architecture: Independent Jobs

All 7 workflows are implemented as **independent Dagster jobs**. They don't automatically chain together yet. Here's how to use them:

## Manual Job Execution

### 1. Ingest Fixtures (Daily)
```python
# Via Dagster UI: Go to Jobs → ingest_fixtures → Launch Run
# Configure:
{
  "ops": {
    "ingest_fixtures_op": {
      "config": {
        "date_str": "2024-01-15",  # Optional
        "team_ids": "541,529"       # Optional
      }
    }
  }
}
```

### 2. Monitor Fixtures (Every 3 minutes via schedule)
```python
# Runs automatically every 3 minutes
# Or manually: Jobs → monitor_fixtures → Launch Run
# No config needed
```

### 3. Process Goals (Triggered by monitor results)
```python
# After monitor runs, check MongoDB for fixtures with goals
# Then manually trigger:
{
  "ops": {
    "validate_and_store_goals_op": {
      "config": {
        "fixture_id": 12345,
        "goal_events": [...]  # Goal events from API
      }
    }
  }
}
```

### 4. Scrape Twitter (For each new goal)
```python
{
  "ops": {
    "search_twitter_videos_op": {
      "config": {
        "goal_id": "12345_67"
      }
    }
  }
}
```

### 5. Download Videos (After Twitter finds videos)
```python
{
  "ops": {
    "get_videos_to_download_op": {
      "config": {
        "goal_id": "12345_67"
      }
    }
  }
}
```

### 6. Filter Videos (After downloads complete)
```python
{
  "ops": {
    "deduplicate_videos_op": {
      "config": {
        "goal_id": "12345_67"
      }
    }
  }
}
```

### 7. Advance Fixtures (Manual or scheduled)
```python
{
  "ops": {
    "advance_fixtures_op": {
      "config": {
        "source_collection": "fixtures_staging",
        "destination_collection": "fixtures_active",
        "fixture_id": 12345  # Optional
      }
    }
  }
}
```

## Automation Options

### Option A: Enable Sensors (Recommended)
Sensors watch for job completions and automatically trigger downstream jobs.

1. Implement sensors in `src/sensors/__init__.py` (template created)
2. Add sensors to Definitions
3. Enable sensors in Dagster UI

### Option B: Python Script for Chaining
Create a Python script that:
1. Runs monitor_fixtures_job
2. Queries MongoDB for results
3. Triggers downstream jobs programmatically

### Option C: External Orchestration
Use another tool (Airflow, Prefect, cron + Python) to:
1. Run monitor on schedule
2. Check results
3. Trigger downstream jobs via Dagster API

## Why Jobs Don't Show Dependencies in UI

Dagster jobs are **independent execution units**. To see dependency graphs in the UI, you need:

1. **Assets with dependencies** - Assets can depend on other assets
2. **Multi-op jobs** - Show ops within a job (but not job-to-job)
3. **Sensors** - Don't show in graph, but automate triggering

## Recommended Next Step

**Implement the sensors in `src/sensors/__init__.py`** to automatically chain jobs:
- Monitor runs → Check MongoDB → Trigger process_goals for each fixture
- Process goals completes → Trigger scrape_twitter for each goal
- Scrape twitter completes → Trigger download_videos
- Download completes → Trigger filter_videos

This keeps jobs independent (can still trigger manually) but adds automation.
