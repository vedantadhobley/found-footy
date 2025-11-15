"""
Found Footy - Football highlights automation with Dagster

Main Dagster Definitions object combining jobs, schedules, and resources.

Architecture:
- Core jobs: ingest_fixtures, advance_fixtures, monitor_fixtures
- Pipeline jobs: goal_pipeline → twitter_search → download_and_upload_videos → deduplicate_videos
- monitor_fixtures runs every 5 min, triggers goal_pipeline with fixture data
- Complete pipeline: goal → Twitter → download → S3 upload → deduplication
"""
from dagster import Definitions

# Import jobs
from src.jobs import (
    ingest_fixtures_job,
    advance_fixtures_job,
    monitor_fixtures_job,
    goal_pipeline_job,
    twitter_search_job,
    download_and_upload_videos_job,
    deduplicate_videos_job,
    test_pipeline_job,
)

# Import schedules
from src.schedules import daily_ingest_schedule, advance_schedule, monitor_schedule

# Import resources
from src.resources import (
    mongo_resource,
    s3_resource,
    twitter_session_resource,
)


# Combine everything into Dagster Definitions
defs = Definitions(
    jobs=[
        # Core jobs
        ingest_fixtures_job,    # Fetch fixtures from API-Football (scheduled daily)
        advance_fixtures_job,   # Move fixtures staging → active after ingest
        monitor_fixtures_job,   # Monitor for changes (scheduled every 5 min)
        
        # Pipeline jobs
        goal_pipeline_job,      # Process goals → trigger Twitter search
        twitter_search_job,     # Search Twitter → trigger download
        download_and_upload_videos_job,  # Download + upload → trigger deduplication
        deduplicate_videos_job, # Deduplicate videos using OpenCV
        
        # Test job
        test_pipeline_job,      # Test job: Insert fixture → Trigger monitor
    ],
    schedules=[
        daily_ingest_schedule,  # Ingest fixtures at midnight UTC
        advance_schedule,       # Advance fixtures at kickoff (every minute check)
        monitor_schedule,       # Monitor every 5 minutes
    ],
    resources={
        "mongo": mongo_resource,
        "s3": s3_resource,
        "twitter_session": twitter_session_resource,
    },
)
