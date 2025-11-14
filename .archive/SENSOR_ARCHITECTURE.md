# Sensor-Based Architecture

## Overview

The system now uses **sensors** to monitor MongoDB and trigger jobs in parallel. This gives you proper separation of concerns and the ability to see the full pipeline flow.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      SENSOR CHAIN                           │
└─────────────────────────────────────────────────────────────┘

1. monitor_fixtures_sensor (every 3 min)
   ├─ Polls MongoDB for fixtures with goals
   ├─ Triggers process_goals_job for EACH fixture (parallel)
   └─ Tags: fixture_id, goal_count

2. scrape_twitter_sensor (every 1 min)
   ├─ Polls MongoDB for goals needing Twitter scraping
   ├─ Triggers scrape_twitter_job for EACH goal (parallel)
   └─ Tags: goal_id, fixture_id

3. download_videos_sensor (every 1 min)
   ├─ Polls MongoDB for goals with videos to download
   ├─ Triggers download_videos_job for EACH goal (parallel)
   └─ Tags: goal_id, video_count

4. filter_videos_sensor (every 2 min)
   ├─ Polls MongoDB for goals with downloaded videos
   ├─ Triggers filter_videos_job for EACH goal (parallel)
   └─ Tags: goal_id, video_count
```

## Key Benefits

### ✅ Proper Separation
- **Monitoring** happens in sensors (not in jobs)
- **Processing** happens in jobs (one job = one task)
- Monitor **triggers** the pipeline, doesn't **run** it

### ✅ Parallel Execution
- Sensors trigger multiple jobs simultaneously
- 10 goals = 10 parallel scrape_twitter jobs
- 5 fixtures = 5 parallel process_goals jobs

### ✅ Visibility
- Each sensor shows what it's monitoring
- Each job shows its specific task
- Dagster UI shows the full chain of triggers

### ✅ Idempotency
- Sensors use `run_key` to prevent duplicate runs
- MongoDB `processing_status` tracks progress
- Safe to re-run any job at any time

## Jobs (Each Does ONE Thing)

### 7 Individual Jobs
1. **ingest_fixtures_job** - Fetch fixtures from API-Football
2. **advance_fixtures_job** - Move fixtures between collections
3. **process_goals_job** - Validate and store goal events
4. **scrape_twitter_job** - Search Twitter for one goal's videos
5. **download_videos_job** - Download videos for one goal with yt-dlp
6. **filter_videos_job** - Deduplicate videos for one goal with OpenCV
7. **monitor_fixtures_job** - Legacy manual monitoring (replaced by sensor)

### 1 Sequential Pipeline (Alternative)
- **goal_processing_pipeline** - Runs all steps sequentially for ALL goals
- Use this if you want to trigger the full pipeline manually
- Sensors are better for continuous automation

## How It Works

### Automated Flow (Sensors)
```
1. monitor_fixtures_sensor runs every 3 minutes
   └─> Finds fixtures with goals
   └─> Triggers process_goals_job for EACH fixture in parallel

2. process_goals_job runs
   └─> Stores goals in MongoDB with processing_status

3. scrape_twitter_sensor runs every 1 minute
   └─> Finds goals where processing_status.twitter_scraped = False
   └─> Triggers scrape_twitter_job for EACH goal in parallel

4. scrape_twitter_job runs
   └─> Searches Twitter, stores videos in MongoDB
   └─> Updates processing_status.twitter_scraped = True

5. download_videos_sensor runs every 1 minute
   └─> Finds goals where twitter_scraped=True, videos_downloaded=False
   └─> Triggers download_videos_job for EACH goal in parallel

6. download_videos_job runs
   └─> Downloads videos with yt-dlp, uploads to S3
   └─> Updates processing_status.videos_downloaded = True

7. filter_videos_sensor runs every 2 minutes
   └─> Finds goals where videos_downloaded=True, videos_filtered=False
   └─> Triggers filter_videos_job for EACH goal in parallel

8. filter_videos_job runs
   └─> Deduplicates videos with OpenCV
   └─> Updates processing_status.videos_filtered = True
   └─> Updates processing_status.completed = True
```

### Manual Execution
You can still run any individual job manually:
```bash
# Process a specific fixture's goals
dagster job execute -j process_goals_job -c '{"ops": {"validate_and_store_goals_op": {"config": {"fixture_id": "...", "goal_events": [...]}}}}'

# Scrape Twitter for a specific goal
dagster job execute -j scrape_twitter_job -c '{"ops": {"search_twitter_videos_op": {"config": {"goal_id": "..."}}}}'

# Download videos for a specific goal
dagster job execute -j download_videos_job -c '{"ops": {"get_videos_to_download_op": {"config": {"goal_id": "..."}}}}'

# Filter videos for a specific goal
dagster job execute -j filter_videos_job -c '{"ops": {"get_videos_to_filter_op": {"config": {"goal_id": "..."}}}}'
```

## Enabling/Disabling Sensors

In the Dagster UI:
1. Go to **Automation** → **Sensors**
2. Click on any sensor
3. Toggle **Running** to enable/disable

All sensors start **disabled** by default. You must enable them in the UI.

## Monitoring

### Check Sensor Status
- Dagster UI → Automation → Sensors
- See when each sensor last ran
- See what jobs were triggered

### Check Job Runs
- Dagster UI → Runs
- Filter by job name
- See tags (fixture_id, goal_id, etc.)

### Check MongoDB Status
```python
# Find goals in each stage
db.goals.count_documents({"processing_status.twitter_scraped": False})
db.goals.count_documents({"processing_status.videos_downloaded": False})
db.goals.count_documents({"processing_status.videos_filtered": False})
db.goals.count_documents({"processing_status.completed": True})
```

## Migration from Schedule-Based

### Old Way (Schedule)
```python
# monitor_schedule runs every 3 minutes
# monitor_fixtures_job logs what it finds
# Nothing happens automatically
```

### New Way (Sensors)
```python
# monitor_fixtures_sensor runs every 3 minutes
# Triggers process_goals_job for EACH fixture with goals
# Sensors cascade: process → scrape → download → filter
# Everything happens automatically in parallel
```

## Configuration

### Sensor Intervals
Edit `src/sensors/__init__.py`:
```python
@sensor(
    name="monitor_fixtures_sensor",
    minimum_interval_seconds=180,  # Change this
    ...
)
```

### Parallel Limits
Edit sensor queries:
```python
goals = db.goals.find(...).limit(20)  # Process up to 20 at a time
```

### MongoDB Connection
Sensors use hardcoded connection (same as jobs):
```python
MongoClient("mongodb://localhost:27017")
```

## Troubleshooting

### Sensors Not Triggering
- Check if sensors are **enabled** in UI
- Check sensor logs in UI
- Verify MongoDB has data matching queries

### Jobs Failing
- Check job logs in UI
- Verify Config classes match job expectations
- Check MongoDB processing_status fields

### Duplicate Runs
- Sensors use `run_key` for idempotency
- Same fixture+goal_count = same run_key = won't re-run
- Delete cursor to force re-evaluation

## Next Steps

1. **Enable sensors** in Dagster UI (Automation → Sensors)
2. **Run ingest_fixtures_job** to populate MongoDB
3. **Watch sensors trigger jobs** automatically
4. **Monitor progress** in Dagster UI → Runs
5. **Check MongoDB** for processing_status updates
