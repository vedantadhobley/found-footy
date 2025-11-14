"""
Found Footy - Football highlights automation with Dagster

Main Dagster Definitions object combining all assets, jobs, schedules, and resources.
"""
from dagster import Definitions

# Import all assets
from src.assets.fixtures.ingest import ingest_fixtures_asset
from src.assets.fixtures.monitor import monitor_fixtures_asset
from src.assets.fixtures.advance import advance_fixtures_asset
from src.assets.goals.process_goals import process_goals_asset
from src.assets.twitter.scrape import scrape_twitter_videos_asset
from src.assets.videos.download import download_videos_asset
from src.assets.videos.filter import filter_videos_asset

# Import jobs
from src.jobs import (
    fixtures_pipeline_job,
    goal_processing_job,
    twitter_scraping_job,
    video_pipeline_job
)

# Import schedules
from src.schedules import (
    daily_ingest_schedule,
    monitor_schedule
)

# Import resources
from src.resources import (
    mongo_resource,
    s3_resource,
    twitter_session_resource
)


# Combine everything into Dagster Definitions
defs = Definitions(
    assets=[
        # Fixtures
        ingest_fixtures_asset,
        monitor_fixtures_asset,
        advance_fixtures_asset,
        # Goals
        process_goals_asset,
        # Twitter
        scrape_twitter_videos_asset,
        # Videos
        download_videos_asset,
        filter_videos_asset,
    ],
    jobs=[
        fixtures_pipeline_job,
        goal_processing_job,
        twitter_scraping_job,
        video_pipeline_job,
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
