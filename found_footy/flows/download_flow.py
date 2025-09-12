import tempfile  # ✅ ADD: Missing import
import os        # ✅ ADD: Missing import
import json      # ✅ ADD: Missing import
from prefect import flow, task, get_run_logger
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.storage.s3_store import FootyS3Store
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
import yt_dlp   # ✅ ADD: Missing import

store = FootyMongoStore()
s3_store = FootyS3Store()

@task(name="download-videos-task")
def download_videos_task(goal_id: str) -> Dict[str, Any]:
    """Download videos using yt-dlp Python API and upload to S3"""
    logger = get_run_logger()
    
    logger.info(f"📥 Starting yt-dlp Python API download for goal: {goal_id}")
    
    try:
        goal_doc = store.goals_pending.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
            return {"status": "not_found", "goal_id": goal_id}
        
        discovered_videos = goal_doc.get("discovered_videos", [])
        if not discovered_videos:
            logger.warning(f"⚠️ No discovered tweet URLs for goal {goal_id}")
            return {"status": "no_videos", "goal_id": goal_id}
        
        logger.info(f"📥 Processing {len(discovered_videos)} tweet URLs for {goal_doc['team_name']} - {goal_doc['player_name']}")
        
        # Download and upload each video
        successful_uploads = []
        failed_downloads = []
        
        for video_info in discovered_videos:
            tweet_url = video_info["tweet_url"]
            search_index = video_info.get("search_index", 0)
            video_index = video_info.get("video_index", 0)
            
            logger.info(f"📥 Using yt-dlp Python API to download: {tweet_url}")
            
            try:
                # ✅ USE YT-DLP PYTHON API
                with tempfile.TemporaryDirectory() as temp_dir:
                    
                    # Step 1: Configure yt-dlp options
                    output_template = os.path.join(temp_dir, f"{goal_id}_{search_index}_{video_index}.%(ext)s")
                    
                    ydl_opts = {
                        'format': 'best[height<=720]',  # Prefer 720p or lower
                        'outtmpl': output_template,
                        'writeinfojson': True,  # Save metadata
                        'quiet': True,  # Reduce verbose output
                        'no_warnings': True,
                    }
                    
                    logger.info(f"🔍 Getting video info with yt-dlp: {tweet_url}")
                    
                    # Step 2: Extract video info first
                    with yt_dlp.YoutubeDL({'quiet': True, 'no_warnings': True}) as ydl:
                        try:
                            video_info_dict = ydl.extract_info(tweet_url, download=False)
                            
                            if not video_info_dict:
                                logger.error(f"❌ yt-dlp could not extract info for {tweet_url}")
                                failed_downloads.append({
                                    **video_info,
                                    "error": "yt-dlp info extraction failed",
                                    "failed_at": datetime.now(timezone.utc).isoformat()
                                })
                                continue
                            
                            actual_video_url = video_info_dict.get('url', tweet_url)
                            
                            # Extract metadata
                            video_metadata = {
                                "resolution": f"{video_info_dict.get('width', 'unknown')}x{video_info_dict.get('height', 'unknown')}",
                                "duration": video_info_dict.get('duration', 'unknown'),
                                "format": video_info_dict.get('ext', 'mp4'),
                                "quality": f"{video_info_dict.get('height', 'unknown')}p",
                                "title": video_info_dict.get('title', ''),
                                "uploader": video_info_dict.get('uploader', ''),
                                "filesize": video_info_dict.get('filesize', 'unknown'),
                                "extracted_by": "yt-dlp_python"
                            }
                            
                        except Exception as e:
                            logger.error(f"❌ yt-dlp info extraction failed for {tweet_url}: {e}")
                            failed_downloads.append({
                                **video_info,
                                "error": f"yt-dlp info extraction failed: {str(e)}",
                                "failed_at": datetime.now(timezone.utc).isoformat()
                            })
                            continue
                    
                    # Step 3: Download video file
                    logger.info(f"📥 Downloading video with yt-dlp: {tweet_url}")
                    
                    with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                        try:
                            ydl.download([tweet_url])
                            
                        except Exception as e:
                            logger.error(f"❌ yt-dlp download failed for {tweet_url}: {e}")
                            failed_downloads.append({
                                **video_info,
                                "error": f"yt-dlp download failed: {str(e)}",
                                "failed_at": datetime.now(timezone.utc).isoformat()
                            })
                            continue
                    
                    # Step 4: Find downloaded file
                    downloaded_files = [f for f in os.listdir(temp_dir) if f.startswith(f"{goal_id}_{search_index}_{video_index}") and not f.endswith('.info.json')]
                    
                    if not downloaded_files:
                        logger.error(f"❌ No downloaded file found for {tweet_url}")
                        failed_downloads.append({
                            **video_info,
                            "error": "No downloaded file found",
                            "failed_at": datetime.now(timezone.utc).isoformat()
                        })
                        continue
                    
                    downloaded_file = os.path.join(temp_dir, downloaded_files[0])
                    file_size = os.path.getsize(downloaded_file)
                    
                    logger.info(f"✅ Downloaded {downloaded_files[0]} ({file_size} bytes)")
                    
                    # Step 5: Upload to S3
                    upload_result = s3_store.upload_video_file(
                        downloaded_file,
                        goal_id,
                        search_index,
                        video_index,
                        metadata={
                            "search_term": video_info["search_term"],
                            "source_tweet_id": video_info.get("tweet_id"),
                            "source_tweet_url": video_info.get("tweet_url"),
                            "video_resolution": video_metadata.get("resolution", "unknown"),
                            "video_duration": video_metadata.get("duration", "unknown"),
                            "video_format": video_metadata.get("format", "mp4"),
                            "video_quality": video_metadata.get("quality", "unknown"),
                            "extracted_by": "yt-dlp_python",
                            "discovered_at": video_info.get("discovered_at")
                        }
                    )
                    
                    if upload_result["status"] == "success":
                        successful_uploads.append({
                            **video_info,
                            "s3_key": upload_result["s3_key"],
                            "s3_url": upload_result["s3_url"],
                            "bucket": upload_result["bucket"],
                            "file_size": upload_result["file_size"],
                            "upload_status": "uploaded_to_s3",
                            "upload_metadata": upload_result["metadata"],
                            "downloaded_at": datetime.now(timezone.utc).isoformat(),
                            "actual_video_url": actual_video_url,
                            "yt_dlp_metadata": video_metadata
                        })
                        
                        logger.info(f"✅ Uploaded to S3: {upload_result['s3_key']}")
                        
                    else:
                        error_msg = upload_result.get("error", "Unknown S3 error")
                        failed_downloads.append({
                            **video_info,
                            "error": f"S3 upload failed: {error_msg}",
                            "failed_at": datetime.now(timezone.utc).isoformat()
                        })
                        
                        logger.error(f"❌ S3 upload failed for {tweet_url}: {error_msg}")
                    
                    # File automatically cleaned up when temp_dir exits
                    
            except Exception as e:
                failed_downloads.append({
                    **video_info,
                    "error": str(e),
                    "failed_at": datetime.now(timezone.utc).isoformat()
                })
                
                logger.error(f"❌ Exception processing {tweet_url}: {e}")
        
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
        goal_doc["download_method"] = "yt-dlp_python"
        
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
            "storage_backend": "s3",
            "download_method": "yt-dlp_python"
        }
        
    except Exception as e:
        logger.error(f"❌ Error downloading videos for goal {goal_id}: {e}")
        return {"status": "error", "goal_id": goal_id, "error": str(e)}

@flow(
    name="download-flow"
)
def download_flow(goal_id: Optional[str] = None):
    """Video download flow using yt-dlp Python API"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}
    
    logger.info(f"📥 Starting yt-dlp Python API download for goal: {goal_id}")
    
    # Download videos using yt-dlp Python API and upload to S3
    download_result = download_videos_task(goal_id)
    
    logger.info(f"✅ Video download completed for goal {goal_id}")
    
    return {
        "goal_id": goal_id,
        "download_result": download_result,
        "status": "completed",
        "storage_backend": "s3",
        "download_method": "yt-dlp_python"
    }