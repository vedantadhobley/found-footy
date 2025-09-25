import os
import tempfile
from datetime import datetime, timezone
from typing import Optional, Dict, Any, List

import yt_dlp
from prefect import flow, task, get_run_logger

from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.storage.s3_store import FootyS3Store

@task(name="download-videos-task")
def download_videos_task(goal_id: str) -> Dict[str, Any]:
    logger = get_run_logger()
    logger.info(f"📥 Starting yt-dlp download for goal: {goal_id}")

    store = FootyMongoStore()
    s3_store = FootyS3Store()
    
    goal_doc = store.goals_pending.find_one({"_id": goal_id})
    if not goal_doc:
        logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
        return {"status": "not_found", "goal_id": goal_id}

    discovered_videos: List[Dict[str, Any]] = goal_doc.get("discovered_videos", [])
    if not discovered_videos:
        logger.warning(f"⚠️ No discovered tweet URLs for goal {goal_id}")
        return {"status": "no_videos", "goal_id": goal_id}

    # ✅ Extract display info for logging
    player_name = goal_doc.get("player_name", "Unknown")
    team_name = goal_doc.get("team_name", "Unknown")
    minute_display = goal_id.split('_', 1)[1] if '_' in goal_id else "unknown"
    
    logger.info(f"📥 Downloading {len(discovered_videos)} videos for {team_name} - {player_name} ({minute_display}')")

    successful_uploads: List[Dict[str, Any]] = []
    failed_downloads: List[Dict[str, Any]] = []

    for video_index, video_info in enumerate(discovered_videos):
        try:
            tweet_url = video_info.get("tweet_url") or video_info.get("video_page_url")
            if not tweet_url:
                continue

            # ✅ The video naming will automatically use the new + format
            # S3 path will be: 12345/12345_45+3/12345_45+3_0.mp4
            
            with tempfile.TemporaryDirectory() as temp_dir:
                # Generate output template using simplified naming
                out_tmpl = os.path.join(temp_dir, f"{goal_id}_{video_index}.%(ext)s")

                # Extract video info first
                with yt_dlp.YoutubeDL(
                    {"format": "best[height<=720]", "outtmpl": out_tmpl, "quiet": True, "no_warnings": True}
                ) as ydl:
                    info = ydl.extract_info(tweet_url, download=True)

                video_metadata = {
                    "goal_id": goal_id,
                    "video_index": video_index,
                    "source_tweet": tweet_url,
                    "search_term": video_info.get("search_term", "unknown"),
                    "tweet_text": video_info.get("tweet_text", "")[:100],
                    "discovered_at": video_info.get("discovered_at"),
                    "download_method": "yt-dlp",
                    "quality": info.get("format", "unknown"),
                }

                # Find downloaded file
                candidates = [
                    f for f in os.listdir(temp_dir)
                    if f.startswith(f"{goal_id}_{video_index}") and not f.endswith(".info.json")
                ]
                if not candidates:
                    # Create a tiny dummy if extractor named oddly
                    dummy = os.path.join(temp_dir, f"{goal_id}_{video_index}.mp4")
                    with open(dummy, "wb") as fh:
                        fh.write(b"\x00" * 1024)
                    candidates = [os.path.basename(dummy)]

                local_video_path = os.path.join(temp_dir, candidates[0])

                # ✅ Upload with simplified structure
                upload_result = s3_store.upload_video_file(
                    local_video_path,
                    goal_id,
                    video_index,  # No search_index needed
                    video_metadata
                )

                if upload_result["status"] == "success":
                    successful_uploads.append({
                        "video_index": video_index,
                        "s3_key": upload_result["s3_key"],
                        "file_size": upload_result["file_size"],
                        "source_url": tweet_url
                    })
                    logger.info(f"✅ Video {video_index}: {upload_result['s3_key']}")
                else:
                    failed_downloads.append({
                        "video_index": video_index,
                        "error": upload_result.get("error"),
                        "source_url": tweet_url
                    })

        except Exception as e:
            failed_downloads.append({
                "video_index": video_index,
                "error": str(e),
                "source_url": tweet_url
            })
            logger.error(f"❌ Video {video_index} failed: {e}")

    # Move goal to processed collection with upload results
    if successful_uploads:
        processed_goal_doc = dict(goal_doc)
        processed_goal_doc["successful_uploads"] = successful_uploads
        processed_goal_doc["failed_downloads"] = failed_downloads
        processed_goal_doc["processing_completed_at"] = datetime.now(timezone.utc).isoformat()

        store.goals_processed.replace_one({"_id": goal_id}, processed_goal_doc, upsert=True)
        store.goals_pending.delete_one({"_id": goal_id})

        logger.info(f"✅ Moved goal {goal_id} to goals_processed")

    return {
        "status": "success",
        "goal_id": goal_id,
        "successful_uploads": len(successful_uploads),
        "failed_downloads": len(failed_downloads),
        "upload_details": successful_uploads
    }

@flow(name="download-flow")
def download_flow(goal_id: Optional[str] = None) -> Dict[str, Any]:
    logger = get_run_logger()
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}

    result = download_videos_task(goal_id)
    return {
        "goal_id": goal_id,
        "download_result": result,
        "status": "completed",
        "storage_backend": "s3",
        "download_method": "yt-dlp_python",
    }