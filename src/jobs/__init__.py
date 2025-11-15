"""Dagster jobs - event-driven workflows for Found Footy

Jobs are used for workflows that are programmatically triggered with runtime parameters,
as opposed to assets which define data dependencies.

The complete workflow:
- ingest_fixtures_job (scheduled daily) → stores fixtures by status
- advance_fixtures_job (triggered) → moves fixtures between collections
- monitor_fixtures_job (scheduled every 5min) → detects goal changes → triggers goal_pipeline_job
- goal_pipeline_job → stores goals → triggers twitter_search_job for new goals
- twitter_search_job → searches Twitter → triggers download_and_upload_videos_job
- download_and_upload_videos_job → downloads videos with yt-dlp → uploads to S3 → triggers deduplicate_videos_job
- deduplicate_videos_job → deduplicates using OpenCV → deletes duplicates from S3
"""

from .ingest_fixtures import ingest_fixtures_job
from .advance import advance_fixtures_job
from .monitor import monitor_fixtures_job
from .goal_pipeline import goal_pipeline_job
from .twitter_search import twitter_search_job
from .download_videos import download_and_upload_videos_job
from .deduplicate_videos import deduplicate_videos_job
from .test_pipeline import test_pipeline_job

__all__ = [
    "ingest_fixtures_job",
    "advance_fixtures_job",
    "monitor_fixtures_job",
    "goal_pipeline_job",
    "twitter_search_job",
    "download_and_upload_videos_job",
    "deduplicate_videos_job",
    "test_pipeline_job",
]
