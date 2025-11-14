"""Download and upload video ops - separate Twitter download from S3 upload"""

from dagster import op, Config, Backoff, Jitter, RetryPolicy
from typing import Dict, Any, List
from pymongo import MongoClient
from bson import ObjectId
from datetime import datetime
import subprocess
import tempfile
from pathlib import Path


class MongoConfig(Config):
    """MongoDB connection configuration."""
    mongo_uri: str = "mongodb://localhost:27017"
    db_name: str = "found_footy"


@op(
    name="download_videos",
    description="Download videos from Twitter for a single goal",
    retry_policy=RetryPolicy(
        max_retries=3,
        delay=15,  # 15 seconds between retries
        backoff=Backoff.EXPONENTIAL,
        jitter=Jitter.PLUS_MINUS
    )
)
def download_videos_op(context, config: MongoConfig, scrape_result: Dict) -> Dict[str, Any]:
    """
    Download videos from Twitter for ONE goal using yt-dlp.
    
    Retries up to 3 times on failure (network/Twitter issues).
    Downloads are stored temporarily and paths returned for upload op.
    """
    goal_id = scrape_result["goal_id"]
    player = scrape_result["player"]
    minute = scrape_result["minute"]
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Get videos for THIS goal that need downloading
    videos = list(db.videos.find({
        "goal_id": ObjectId(goal_id),
        "download_status": "pending"
    }))
    
    if not videos:
        context.log.info("No videos to download")
        client.close()
        return {
            "goal_id": goal_id,
            "player": player,
            "minute": minute,
            "downloaded_count": 0,
            "failed_count": 0,
            "video_paths": []
        }
    
    context.log.info(f"📥 Downloading {len(videos)} videos for {player} ({minute}')")
    
    downloaded_videos = []
    failed_count = 0
    
    # Create temp directory for this goal's videos
    tmpdir = tempfile.mkdtemp(prefix=f"goal_{goal_id}_")
    
    try:
        for video in videos:
            video_url = video["video_url"]
            video_id = str(video["_id"])
            
            try:
                output_path = Path(tmpdir) / f"{video_id}.mp4"
                
                # Download with yt-dlp
                cmd = [
                    "yt-dlp",
                    "-f", "best",
                    "-o", str(output_path),
                    video_url
                ]
                
                result = subprocess.run(cmd, capture_output=True, text=True, timeout=120)
                
                if result.returncode == 0 and output_path.exists():
                    # Mark as downloaded in DB
                    db.videos.update_one(
                        {"_id": video["_id"]},
                        {"$set": {
                            "download_status": "completed",
                            "downloaded_at": datetime.utcnow()
                        }}
                    )
                    
                    downloaded_videos.append({
                        "video_id": video_id,
                        "local_path": str(output_path),
                        "video_url": video_url
                    })
                    
                    context.log.info(f"✅ Downloaded video {video_id}")
                else:
                    db.videos.update_one(
                        {"_id": video["_id"]},
                        {"$set": {"download_status": "failed"}}
                    )
                    failed_count += 1
                    context.log.error(f"❌ yt-dlp failed for {video_url}")
            
            except Exception as e:
                context.log.error(f"Failed to download {video_id}: {e}")
                db.videos.update_one(
                    {"_id": video["_id"]},
                    {"$set": {"download_status": "failed"}}
                )
                failed_count += 1
        
        # Update goal processing status
        if downloaded_videos:
            db.goals.update_one(
                {"_id": ObjectId(goal_id)},
                {"$set": {"processing_status.videos_downloaded": True}}
            )
        
        client.close()
        
        context.log.info(f"✅ Downloaded {len(downloaded_videos)}/{len(videos)} videos")
        
        return {
            "goal_id": goal_id,
            "player": player,
            "minute": minute,
            "downloaded_count": len(downloaded_videos),
            "failed_count": failed_count,
            "video_paths": downloaded_videos,
            "temp_dir": tmpdir  # Pass to upload op for cleanup
        }
    
    except Exception as e:
        client.close()
        context.log.error(f"❌ Download failed: {e}")
        raise  # Re-raise to trigger retry


@op(
    name="upload_videos",
    description="Upload downloaded videos to S3/MinIO",
    retry_policy=RetryPolicy(
        max_retries=3,
        delay=10,
        backoff=Backoff.EXPONENTIAL,
        jitter=Jitter.PLUS_MINUS
    )
)
def upload_videos_op(context, config: MongoConfig, download_result: Dict) -> Dict[str, Any]:
    """
    Upload downloaded videos to S3/MinIO storage.
    
    Retries up to 3 times on failure (network/S3 issues).
    Separate from download so failures can be retried independently.
    """
    from found_footy.storage.s3_store import S3Store
    import shutil
    
    goal_id = download_result["goal_id"]
    player = download_result["player"]
    minute = download_result["minute"]
    video_paths = download_result["video_paths"]
    temp_dir = download_result.get("temp_dir")
    
    if not video_paths:
        context.log.info("No videos to upload")
        return {
            "goal_id": goal_id,
            "player": player,
            "minute": minute,
            "uploaded_count": 0,
            "failed_count": 0
        }
    
    context.log.info(f"☁️ Uploading {len(video_paths)} videos to S3")
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    s3 = S3Store()
    
    uploaded_count = 0
    failed_count = 0
    
    try:
        for video_data in video_paths:
            video_id = video_data["video_id"]
            local_path = video_data["local_path"]
            
            try:
                # Upload to S3
                s3_key = f"goals/{goal_id}/{video_id}.mp4"
                s3.upload_file(local_path, s3_key)
                
                # Update video record with S3 location
                db.videos.update_one(
                    {"_id": ObjectId(video_id)},
                    {"$set": {
                        "upload_status": "completed",
                        "s3_key": s3_key,
                        "uploaded_at": datetime.utcnow()
                    }}
                )
                
                uploaded_count += 1
                context.log.info(f"✅ Uploaded {video_id} to {s3_key}")
            
            except Exception as e:
                context.log.error(f"Failed to upload {video_id}: {e}")
                db.videos.update_one(
                    {"_id": ObjectId(video_id)},
                    {"$set": {"upload_status": "failed"}}
                )
                failed_count += 1
        
        # Update goal processing status
        if uploaded_count > 0:
            db.goals.update_one(
                {"_id": ObjectId(goal_id)},
                {"$set": {"processing_status.videos_uploaded": True}}
            )
        
        client.close()
        
        # Cleanup temp directory
        if temp_dir and Path(temp_dir).exists():
            shutil.rmtree(temp_dir)
            context.log.info(f"🗑️ Cleaned up temp dir: {temp_dir}")
        
        context.log.info(f"✅ Uploaded {uploaded_count}/{len(video_paths)} videos")
        
        return {
            "goal_id": goal_id,
            "player": player,
            "minute": minute,
            "uploaded_count": uploaded_count,
            "failed_count": failed_count
        }
    
    except Exception as e:
        client.close()
        context.log.error(f"❌ Upload failed: {e}")
        raise  # Re-raise to trigger retry
