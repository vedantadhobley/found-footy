"""
Found Footy - Football highlights automation with Dagster

Main Dagster Definitions object combining jobs, schedules, and resources.

Event-Level Debounce Architecture:
1. ingestion_job (daily 00:05 UTC)
   → Fetch fixtures → Categorize by status → Store in staging/active/completed

2. monitor_job (every minute)
   → Activate fixtures → Batch fetch → Update fixtures → Trigger debounce jobs → Complete fixtures

3. debounce_job (per fixture with events)
   → Extract events → Process snapshots → Validate stability → Confirm events → Trigger twitter jobs

4. twitter_job (per confirmed event)
   → Search Twitter → Extract videos → Save discovered_videos

5. download_job (per event with videos)
   → Validate → Download → Deduplicate (OpenCV) → Upload to S3 → Mark completed
"""
from dagster import Definitions

# Import jobs
from src.jobs import (
    ingestion_job,
    ingestion_schedule,
    monitor_job,
    monitor_schedule,
    debounce_job,
    twitter_job,
)

# Import download job
from src.jobs.download.download_job import download_job

# Import resources (if they exist)
try:
    from src.resources import (
        mongo_resource,
        s3_resource,
    )
    has_resources = True
except ImportError:
    has_resources = False


# Combine everything into Dagster Definitions
if has_resources:
    defs = Definitions(
        jobs=[
            ingestion_job,   # Daily fixture ingestion with status-based routing
            monitor_job,     # Per-minute monitoring with in-place event enhancement
            debounce_job,    # Per-fixture event stability validation
            twitter_job,     # Per-event video discovery
            download_job,    # Per-event video download/dedup/upload to S3
        ],
        schedules=[
            ingestion_schedule,  # Daily at 00:05 UTC
            monitor_schedule,    # Every minute
        ],
        resources={
            "mongo": mongo_resource,
            "s3": s3_resource,
        },
    )
else:
    # No resources defined yet
    defs = Definitions(
        jobs=[
            ingestion_job,   # Daily fixture ingestion with status-based routing
            monitor_job,     # Per-minute monitoring with in-place event enhancement
            debounce_job,    # Per-fixture event stability validation
            twitter_job,     # Per-event video discovery
            download_job,    # Per-event video download/dedup/upload to S3
        ],
        schedules=[
            ingestion_schedule,  # Daily at 00:05 UTC
            monitor_schedule,    # Every minute
        ],
    )
