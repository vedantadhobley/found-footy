from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.storage.s3_store import FootyS3Store  # ✅ FIX: Correct import
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
import os

store = FootyMongoStore()
s3_store = FootyS3Store()  # ✅ FIX: Use correct class name

@task(name="download-videos-task")
def download_videos_task(goal_id: str) -> Dict[str, Any]:
    """Download discovered videos and upload to S3"""
    logger = get_run_logger()
    
    logger.info(f"📥 Starting video download for goal: {goal_id}")
    
    try:
        goal_doc = store.goals_pending.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
            return {"status": "not_found", "goal_id": goal_id}
        
        discovered_videos = goal_doc.get("discovered_videos", [])
        if not discovered_videos:
            logger.warning(f"⚠️ No discovered videos for goal {goal_id}")
            return {"status": "no_videos", "goal_id": goal_id}
        
        logger.info(f"📥 Downloading {len(discovered_videos)} videos for {goal_doc['team_name']} - {goal_doc['player_name']}")
        
        # Download and upload each video
        successful_uploads = []
        failed_downloads = []
        
        for video_info in discovered_videos:
            video_url = video_info["video_url"]
            search_index = video_info.get("search_index", 0)
            video_index = video_info.get("video_index", 0)
            
            logger.info(f"📥 Downloading: {video_url}")
            
            try:
                # Download video using yt-dlp and upload to S3
                download_result = s3_store.download_video_from_url(
                    video_url, 
                    goal_id, 
                    f"{search_index}_{video_index}",
                    metadata={
                        "search_term": video_info["search_term"],
                        "source_tweet_id": video_info.get("source_tweet_id"),
                        "source_tweet_url": video_info.get("source_tweet_url"),
                        "video_quality": video_info.get("video_quality"),
                        "duration_seconds": video_info.get("duration_seconds"),
                        "discovered_at": video_info.get("discovered_at")
                    }
                )
                
                if download_result["status"] == "success":
                    s3_info = download_result["s3_info"]
                    
                    successful_uploads.append({
                        **video_info,
                        "s3_key": s3_info["s3_key"],
                        "s3_url": s3_info["s3_url"],
                        "bucket": s3_info["bucket"],
                        "file_size": s3_info["file_size"],
                        "upload_status": "uploaded_to_s3",
                        "upload_metadata": s3_info["metadata"],
                        "downloaded_at": datetime.now(timezone.utc).isoformat()
                    })
                    
                    logger.info(f"✅ Uploaded to S3: {s3_info['s3_key']}")
                    
                else:
                    error_msg = download_result.get("error", "Unknown error")
                    failed_downloads.append({
                        **video_info,
                        "error": error_msg,
                        "failed_at": datetime.now(timezone.utc).isoformat()
                    })
                    
                    logger.error(f"❌ Failed to download {video_url}: {error_msg}")
                    
            except Exception as e:
                failed_downloads.append({
                    **video_info,
                    "error": str(e),
                    "failed_at": datetime.now(timezone.utc).isoformat()
                })
                
                logger.error(f"❌ Exception downloading {video_url}: {e}")
        
        # Update goal document with download results
        goal_doc["download_completed"] = True
        goal_doc["successful_uploads"] = successful_uploads
        goal_doc["failed_downloads"] = failed_downloads
        goal_doc["download_stats"] = {
            "total_discovered": len(discovered_videos),
            "successful": len(successful_uploads),
            "failed": len(failed_downloads),
            "success_rate": len(successful_uploads) / len(discovered_videos) if discovered_videos else 0
        }
        goal_doc["downloaded_at"] = datetime.now(timezone.utc)
        goal_doc["status"] = "download_completed"
        goal_doc["storage_backend"] = "s3"
        goal_doc["s3_bucket"] = s3_store.bucket_name
        
        # Move goal from pending to processed
        store.goals_processed.replace_one({"_id": goal_id}, goal_doc, upsert=True)
        store.goals_pending.delete_one({"_id": goal_id})
        
        logger.info(f"✅ Download completed - {len(successful_uploads)}/{len(discovered_videos)} videos uploaded to S3")
        
        # Log S3 bucket stats
        try:
            bucket_stats = s3_store.get_bucket_stats()
            logger.info(f"📊 S3 Bucket Stats: {bucket_stats['total_videos']} videos, {bucket_stats['total_size_mb']} MB")
        except Exception as e:
            logger.warning(f"⚠️ Could not get S3 stats: {e}")
        
        return {
            "status": "success",
            "goal_id": goal_id,
            "team": goal_doc['team_name'],
            "player": goal_doc['player_name'],
            "minute": goal_doc['minute'],
            "download_stats": goal_doc["download_stats"],
            "successful_uploads": successful_uploads,
            "failed_downloads": failed_downloads,
            "storage_backend": "s3"
        }
        
    except Exception as e:
        logger.error(f"❌ Error downloading videos for goal {goal_id}: {e}")
        return {"status": "error", "goal_id": goal_id, "error": str(e)}

@flow(
    name="download-flow"
    # ❌ NO flow_run_name here - will be set by triggering code
)
def download_flow(goal_id: Optional[str] = None):
    """Video download flow - downloads discovered videos to S3"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}
    
    logger.info(f"📥 Starting video download for goal: {goal_id}")
    
    # Download videos and upload to S3
    download_result = download_videos_task(goal_id)
    
    logger.info(f"✅ Video download completed for goal {goal_id}")
    
    return {
        "goal_id": goal_id,
        "download_result": download_result,
        "status": "completed",
        "storage_backend": "s3"
    }