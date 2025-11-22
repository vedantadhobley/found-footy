"""
Found Footy - Football highlights automation with Dagster

Main Dagster Definitions object combining jobs, schedules, and resources.

New Clean Architecture:
1. ingestion_job (daily 00:05 UTC)
   → Fetch fixtures → Categorize by status → Store in staging/active/completed

2. monitor_job (every minute)
   → Activate fixtures → Batch fetch → Detect goal deltas → Spawn goal jobs → Complete fixtures

3. goal_job (per fixture with new goals)
   → Fetch events → Check status → Add to pending → Validate → Update fixture → Spawn twitter jobs

4. twitter_job (per validated goal)
   → Search Twitter → Extract videos → Save discovered_videos

5. download_job (per goal with videos)
   → Validate → Download → Deduplicate (OpenCV) → Upload to S3 → Mark completed
"""
from dagster import Definitions

# Import jobs
from src.jobs import (
    goal_job,
    ingestion_job,
    ingestion_schedule,
    monitor_job,
    monitor_schedule,
    twitter_job,
)

# Import download job
from src.jobs.download import download_job

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
            monitor_job,     # Per-minute monitoring for goal deltas
            goal_job,        # Per-fixture goal validation
            twitter_job,     # Per-goal video discovery
            download_job,    # Per-goal video download/dedup/upload
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
            monitor_job,     # Per-minute monitoring for goal deltas
            goal_job,        # Per-fixture goal validation
            twitter_job,     # Per-goal video discovery
            download_job,    # Per-goal video download/dedup/upload
        ],
        schedules=[
            ingestion_schedule,  # Daily at 00:05 UTC
            monitor_schedule,    # Every minute
        ],
    )
