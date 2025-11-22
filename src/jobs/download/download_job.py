"""Download Job - Download, deduplicate, and upload goal videos"""
from dagster import Config, job

# Import from twitter job ops (reuse download/upload)
from src.jobs.twitter.ops import download_videos_op, upload_to_s3_op

from .ops import (
    deduplicate_videos_op,
    update_goal_completed_op,
    validate_goal_op,
)


class DownloadJobConfig(Config):
    """Configuration for download job"""
    goal_id: str


@job(
    name="download_job",
    description="Download, deduplicate, and upload goal videos to S3",
    tags={"pipeline": "download", "trigger": "on_demand", "phase": "processing"}
)
def download_job():
    """
    Pipeline to download and process goal videos.
    
    Flow:
    1. Validate goal has discovered_videos
    2. Download videos to temp directory
    3. Deduplicate using OpenCV (while still local)
    4. Upload deduplicated videos to S3
    5. Mark goal as completed
    
    This job is triggered by twitter_job when sufficient videos are discovered.
    
    Benefits of inline deduplication:
    - No re-download from S3 for analysis
    - Only upload deduplicated videos (saves S3 storage)
    - Faster processing (everything in temp dir)
    - Uses Python tempfile.TemporaryDirectory() for auto-cleanup
    
    The temp directory is automatically deleted when job completes.
    Config (goal_id) will be provided at runtime via RunConfig.
    """
    # Note: goal_id from config will be passed to ops at runtime
    
    # Validate goal exists and has videos
    validation_result = validate_goal_op()
    
    # Download videos to temp directory
    downloaded_videos = download_videos_op(validation_result)
    
    # Deduplicate while still local (before S3 upload)
    unique_videos = deduplicate_videos_op(downloaded_videos)
    
    # Upload only deduplicated videos to S3
    upload_result = upload_to_s3_op(unique_videos)
    
    # Mark goal as completed
    update_goal_completed_op(upload_result)
