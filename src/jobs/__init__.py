"""Dagster jobs - event-driven workflows for Found Footy

Jobs are used for workflows that are programmatically triggered with runtime parameters,
as opposed to assets which define data dependencies.

The complete workflow:
- ingest_fixtures_job (scheduled daily) → stores fixtures by status
- advance_fixtures_job (triggered) → moves fixtures between collections
- monitor_fixtures_job (scheduled every 3min) → detects goal changes → triggers process_goals_job
- process_goals_job → validates goals → triggers scrape_twitter_job  
- scrape_twitter_job → finds videos → triggers download_videos_job
- download_videos_job → downloads and stores to S3 → triggers filter_videos_job
- filter_videos_job → deduplicates videos using OpenCV
"""

from .ingest_fixtures import ingest_fixtures_job
from .advance_fixtures import advance_fixtures_job
from .monitor import monitor_fixtures_job
from .process_goals import process_goals_job
from .scrape_twitter import scrape_twitter_job
from .download_videos import download_videos_job
from .filter_videos import filter_videos_job

__all__ = [
    "ingest_fixtures_job",
    "advance_fixtures_job",
    "monitor_fixtures_job",
    "process_goals_job",
    "scrape_twitter_job",
    "download_videos_job",
    "filter_videos_job",
]
