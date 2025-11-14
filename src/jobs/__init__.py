"""Dagster asset jobs - group assets into executable pipelines"""
from dagster import define_asset_job, AssetSelection


# Fixtures pipeline - ingest, monitor, and advance fixtures
fixtures_pipeline_job = define_asset_job(
    name="fixtures_pipeline",
    description="Complete fixtures pipeline: ingest, monitor, and advance",
    selection=AssetSelection.groups("fixtures")
)

# Goal processing - handle discovered goals
goal_processing_job = define_asset_job(
    name="goal_processing",
    description="Process discovered goal events",
    selection=AssetSelection.assets("process_goals")
)

# Twitter scraping - find goal videos
twitter_scraping_job = define_asset_job(
    name="twitter_scraping",
    description="Scrape Twitter for goal videos",
    selection=AssetSelection.assets("scrape_twitter_videos")
)

# Video pipeline - download and filter videos
video_pipeline_job = define_asset_job(
    name="video_pipeline",
    description="Download and deduplicate goal videos",
    selection=AssetSelection.groups("videos")
)
