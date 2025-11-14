"""
Found Footy - Football highlights automation with Dagster

Main Dagster Definitions object combining assets, jobs, schedules, and resources.

Architecture:
- All workflows use Jobs (event-driven or scheduled with runtime parameters)
- Jobs allow runtime configuration via Config classes
- Schedules trigger jobs at specified intervals
"""
from dagster import Definitions

# Import all jobs (all workflows are jobs now since they need runtime params)
from src.jobs import (
    ingest_fixtures_job,
    advance_fixtures_job,
    monitor_fixtures_job,
    process_goals_job,
    scrape_twitter_job,
    download_videos_job,
    filter_videos_job,
)

# Import schedules
from src.schedules import daily_ingest_schedule, monitor_schedule

# Import resources
from src.resources import (
    mongo_resource,
    s3_resource,
    twitter_session_resource,
)


# Combine everything into Dagster Definitions
defs = Definitions(
    assets=[
        # No assets - all workflows are jobs with runtime parameters
    ],
    jobs=[
        # All workflows use jobs for runtime configuration flexibility
        ingest_fixtures_job,
        advance_fixtures_job,
        monitor_fixtures_job,
        process_goals_job,
        scrape_twitter_job,
        download_videos_job,
        filter_videos_job,
    ],
    schedules=[
        daily_ingest_schedule,
        monitor_schedule,
    ],
    resources={
        "mongo": mongo_resource,
        "s3": s3_resource,
        "twitter_session": twitter_session_resource,
    },
)
