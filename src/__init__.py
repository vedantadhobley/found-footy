"""
Found Footy - Football highlights automation with Dagster

Main Dagster Definitions object combining jobs, schedules, and resources.

Architecture:
- 3 jobs: ingest_fixtures, monitor_fixtures, goal_pipeline
- goal_pipeline has 6 ops: process → scrape → download → upload → filter → complete
- monitor_fixtures runs on schedule, triggers goal_pipeline with fixture data
- This matches your Prefect structure: goal_flow → twitter_flow → download_flow → filter_flow
"""
from dagster import Definitions

# Import jobs
from src.jobs import (
    ingest_fixtures_job,
    monitor_fixtures_job,
    goal_pipeline_job,  # Main pipeline with 6 ops
)

# Import sensor
from src.sensors import goal_pipeline_trigger_sensor

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
    jobs=[
        ingest_fixtures_job,    # Fetch fixtures from API-Football (scheduled daily)
        monitor_fixtures_job,   # Monitor for changes (scheduled every 3 min)
        goal_pipeline_job,      # 6 ops: process → scrape → download → upload → filter → complete
    ],
    sensors=[
        goal_pipeline_trigger_sensor,  # Watches for pending goals, triggers goal_pipeline
    ],
    schedules=[
        daily_ingest_schedule,  # Ingest fixtures at midnight
        monitor_schedule,       # Monitor every 3 minutes
    ],
    resources={
        "mongo": mongo_resource,
        "s3": s3_resource,
        "twitter_session": twitter_session_resource,
    },
)
