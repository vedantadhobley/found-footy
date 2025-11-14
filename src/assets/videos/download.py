"""Video download asset - downloads videos from Twitter using yt-dlp"""
from dagster import asset, AssetExecutionContext, Output
import os
import logging

from src.data.mongo_store import FootyMongoStore
from src.data.s3_store import FootyS3Store

logger = logging.getLogger(__name__)


@asset(
    name="download_videos",
    description="Download goal videos from Twitter URLs using yt-dlp",
    group_name="videos",
    compute_kind="download"
)
def download_videos_asset(
    context: AssetExecutionContext,
    goal_id: str
) -> Output:
    """
    Video download asset:
    1. Get discovered video URLs from MongoDB
    2. Download videos using yt-dlp
    3. Upload to S3/MinIO
    4. Update MongoDB with video metadata
    
    TODO: Implement full download logic from download_flow.py
    """
    store = FootyMongoStore()
    s3_store = FootyS3Store()
    
    # Get goal document with discovered videos
    goal_doc = store.goals.find_one({"_id": goal_id})
    
    if not goal_doc:
        context.log.error(f"Goal {goal_id} not found")
        return Output(
            value={"goal_id": goal_id, "videos_downloaded": 0},
            metadata={"status": "error", "error": "Goal not found"}
        )
    
    discovered_videos = goal_doc.get("discovered_videos", [])
    
    if not discovered_videos:
        context.log.warning(f"No videos to download for {goal_id}")
        return Output(
            value={"goal_id": goal_id, "videos_downloaded": 0},
            metadata={"status": "no_videos"}
        )
    
    context.log.info(f"📥 Downloading {len(discovered_videos)} videos for {goal_id}")
    
    # TODO: Implement full download logic:
    # 1. Use yt-dlp to download each video URL
    # 2. Upload to S3/MinIO
    # 3. Store metadata in MongoDB
    # 4. Update processing status
    
    # Placeholder for now
    videos_downloaded = 0
    
    store.update_goal_processing_status(goal_id, "videos_downloaded", video_count=videos_downloaded)
    
    return Output(
        value={"goal_id": goal_id, "videos_downloaded": videos_downloaded},
        metadata={
            "goal_id": goal_id,
            "discovered_count": len(discovered_videos),
            "downloaded_count": videos_downloaded
        }
    )
