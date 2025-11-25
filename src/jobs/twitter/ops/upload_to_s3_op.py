"""Upload videos to S3 storage"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.s3_store import FootyS3Store


@op(
    name="upload_to_s3",
    description="Upload downloaded videos to S3 bucket",
    tags={"kind": "io", "target": "s3"}
)
def upload_to_s3_op(
    context: OpExecutionContext,
    goal_id: str,
    downloaded_videos: List[Dict[str, Any]]
) -> Dict[str, Any]:
    """
    Upload downloaded videos to S3 storage.
    
    Args:
        goal_id: The goal ID these videos belong to
        downloaded_videos: List of video info with local paths
        
    Returns:
        Dict with upload statistics and S3 keys
    """
    if not downloaded_videos:
        context.log.warning("⚠️  No videos to upload")
        return {"status": "no_videos", "uploaded_count": 0}
    
    context.log.info(f"☁️  Uploading {len(downloaded_videos)} videos to S3 for goal {goal_id}")
    
    s3_store = FootyS3Store()
    uploaded_count = 0
    s3_keys = []
    
    for video in downloaded_videos:
        try:
            local_path = video.get("local_path")
            tweet_id = video.get("tweet_id", "unknown")
            
            if not local_path:
                context.log.warning(f"⚠️  Video from tweet {tweet_id} missing local_path")
                continue
            
            # Generate S3 key: videos/{goal_id}/{tweet_id}.mp4
            s3_key = f"videos/{goal_id}/{tweet_id}.mp4"
            
            context.log.info(f"   ☁️  Uploading to s3://found-footy-videos/{s3_key}")
            
            # Upload to S3
            # Extract video index from tweet_id for unique naming
            video_index = uploaded_count
            
            result = s3_store.upload_video_file(
                local_file_path=local_path,
                goal_id=goal_id,
                video_index=video_index,
                metadata={"tweet_id": tweet_id}
            )
            
            s3_url = result.get("url")
            s3_key = result.get("s3_key")
            
            s3_keys.append({"s3_key": s3_key, "s3_url": s3_url, "tweet_id": tweet_id})
            uploaded_count += 1
            
            context.log.info(f"   ✅ Uploaded: {s3_key}")
            
            # Clean up local file
            try:
                from pathlib import Path
                Path(local_path).unlink()
            except Exception as cleanup_error:
                context.log.warning(f"⚠️  Failed to delete local file {local_path}: {cleanup_error}")
            
        except Exception as e:
            context.log.error(f"❌ Error uploading video from tweet {video.get('tweet_id')}: {e}")
            continue
    
    context.log.info(f"✅ Uploaded {uploaded_count} videos to S3")
    
    return {
        "status": "success",
        "uploaded_count": uploaded_count,
        "s3_keys": s3_keys,
        "goal_id": goal_id
    }
