"""Goal Pipeline - Process ONE goal from detection to filtered videos

Architecture: ONE pipeline run per goal (enables parallel processing)

This matches your Prefect flow structure:
- goal_flow → stores goal
- twitter_flow → searches Twitter for videos
- download_flow → downloads videos from Twitter
- upload_flow → uploads videos to S3  
- filter_flow → deduplicates videos

Each op processes ONE goal per run. When 3 goals occur, sensor triggers
3 separate pipeline runs (parallel processing, better isolation).

Logic is split across separate files for maintainability:
- process_goals.py - Store single goal in MongoDB
- scrape_twitter.py - Search Twitter for goal's videos (with retry)
- download_videos.py - Download videos from Twitter (with retry)
  + upload_videos_op - Upload videos to S3 (with retry)  
- filter_videos.py - Deduplicate goal's videos

Parameter Passing:
- Sensor passes goal data via run_config
- process_goal_op reads from run_config and forwards to downstream ops
"""

from dagster import job, op, Config, In, Out, DynamicOut
from typing import Dict, Any
from .process_goals import process_goal_op
from .scrape_twitter import scrape_twitter_op
from .download_videos import download_videos_op, upload_videos_op
from .filter_videos import filter_videos_op


@job(
    name="goal_pipeline",
    description="Complete goal pipeline: process → scrape → download → upload → filter (ONE goal per run)"
)
def goal_pipeline_job():
    """
    Main pipeline with 5 ops for processing ONE goal:
    
    1. process_goal - Store goal in MongoDB (reads full data from fixtures collection)
    2. scrape_twitter - Search Twitter for goal's videos (retry: 3x)
    3. download_videos - Download videos from Twitter (retry: 3x)
    4. upload_videos - Upload videos to S3 (retry: 3x)
    5. filter_videos - Deduplicate goal's videos
    
    When monitor detects 3 goals, sensor triggers 3 pipeline runs in parallel.
    Each run is isolated - failures don't affect other goals.
    Retry policies on external ops (Twitter, S3) for resilience.
    
    Clean parameter flow:
    - Sensor passes minimal config: fixture_id, goal_minute, player_name
    - process_goal reads full data from MongoDB fixtures collection
    - goal_id + metadata flows through pipeline via op returns
    - MongoDB is the source of truth ✅
    """
    process_result = process_goal_op()
    scrape_result = scrape_twitter_op(process_result)
    download_result = download_videos_op(scrape_result)
    upload_result = upload_videos_op(download_result)
    filter_videos_op(upload_result)
