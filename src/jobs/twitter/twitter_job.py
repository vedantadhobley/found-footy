"""Twitter Job - Discover goal videos on Twitter"""
from dagster import Config, job

from .ops import (
    extract_videos_op,
    search_twitter_op,
    update_goal_discovered_videos_op,
)


class TwitterJobConfig(Config):
    """Configuration for twitter job"""
    goal_id: str


@job(
    name="twitter_job",
    description="Search Twitter and discover video URLs for a goal",
    tags={"pipeline": "twitter", "trigger": "on_demand", "phase": "discovery"}
)
def twitter_job():
    """
    Pipeline to discover goal videos on Twitter with smart retry logic.
    
    Flow:
    1. Wait 2 minutes initially for videos to be uploaded
    2. Search Twitter for tweets (player last name + team name)
    3. Retry if < 5 videos found (wait 3 min, then 4 min)
    4. Extract video URLs from tweets
    5. Save discovered_videos to goal document
    
    Smart Features:
    - Initial Delay: 2 minutes for videos to be uploaded after goal
    - Search Query: Uses player last name + team (e.g., "Messi Barcelona")
    - Time Filtering: Only tweets posted AFTER goal's created_at timestamp
    - Retry Logic: If < 5 videos, waits 3min then 4min (total ~10 min window)
    - Saves whatever found: Even if < 5 videos, saves them for download_job
    
    This job is spawned by goal_job after a goal is validated.
    Saves video URLs to discovered_videos field with processing_status='discovered'.
    
    If videos are found, download_job can be manually triggered to:
    - Download videos
    - Deduplicate with OpenCV
    - Upload to S3
    
    Benefits of split:
    - Can rerun twitter search independently if not enough videos found
    - Download/dedup is separate and can be retried independently
    - Clear separation of concerns
    
    NOTE: Includes internal retry mechanisms for Twitter API rate limits.
    Config (goal_id) will be provided at runtime via RunConfig.
    """
    # Note: goal_id from config will be passed to search_twitter_op at runtime
    # The op passes goal_id through the pipeline via return values
    
    # Search Twitter for the goal (returns {goal_id, tweets})
    search_result = search_twitter_op()
    
    # Extract video URLs from tweets (returns {goal_id, videos})
    extraction_result = extract_videos_op(search_result)
    
    # Save discovered videos to goal document
    update_goal_discovered_videos_op(extraction_result)
