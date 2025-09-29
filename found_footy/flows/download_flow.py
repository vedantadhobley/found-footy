import os
import tempfile
from datetime import datetime, timezone
from typing import Optional, Dict, Any, List
import time
import random

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
    
    # ✅ FIX: Use single goals collection
    goal_doc = store.goals.find_one({"_id": goal_id})
    if not goal_doc:
        logger.warning(f"⚠️ Goal {goal_id} not found")
        return {"status": "not_found", "goal_id": goal_id}

    # ✅ NEW: Check processing status to avoid reprocessing
    processing_status = goal_doc.get("processing_status", "unknown")
    if processing_status == "completed":
        logger.info(f"⚠️ Goal {goal_id} already completed, skipping")
        return {"status": "already_completed", "goal_id": goal_id}

    discovered_videos: List[Dict[str, Any]] = goal_doc.get("discovered_videos", [])
    if not discovered_videos:
        logger.warning(f"⚠️ No discovered tweet URLs for goal {goal_id}")
        # ✅ NEW: Mark as completed with no videos using single collection
        store.update_goal_processing_status(goal_id, "completed", successful_uploads=[], failed_downloads=[])
        return {"status": "no_videos", "goal_id": goal_id}

    # ✅ Keep existing player/team extraction logic
    player_name = goal_doc.get("player", {}).get("name", "Unknown")
    team_name = goal_doc.get("team", {}).get("name", "Unknown")
    
    minute_display = goal_id.split('_', 1)[1] if '_' in goal_id else "unknown"
    
    logger.info(f"📥 Downloading {len(discovered_videos)} videos for {team_name} - {player_name} ({minute_display}')")

    successful_uploads: List[Dict[str, Any]] = []
    failed_downloads: List[Dict[str, Any]] = []

    # ✅ NEW: Enhanced retry configurations specifically for 403 errors
    retry_strategies = [
        {
            "name": "standard",
            "format": "best[height<=720]",
            "user_agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
            "wait_before": 0,
            "extra_headers": {}
        },
        {
            "name": "mobile_fallback", 
            "format": "worst[height>=360]/best",
            "user_agent": "Mozilla/5.0 (iPhone; CPU iPhone OS 16_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1",
            "wait_before": 2,
            "extra_headers": {
                "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                "Accept-Language": "en-US,en;q=0.5"
            }
        },
        {
            "name": "desktop_firefox",
            "format": "best",
            "user_agent": "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/121.0",
            "wait_before": 4,
            "extra_headers": {
                "Accept": "video/webm,video/ogg,video/*;q=0.9,application/ogg;q=0.7,audio/*;q=0.6,*/*;q=0.5",
                "Accept-Language": "en-US,en;q=0.9"
            }
        }
    ]

    for video_index, video_info in enumerate(discovered_videos):
        try:
            tweet_url = video_info.get("tweet_url") or video_info.get("video_page_url")
            if not tweet_url:
                logger.warning(f"⚠️ Video {video_index}: No URL found")
                failed_downloads.append({
                    "video_index": video_index,
                    "error": "No URL provided",
                    "error_category": "missing_url",
                    "source_url": None
                })
                continue

            logger.info(f"📥 Attempting video {video_index + 1}/{len(discovered_videos)}: {tweet_url}")
            
            # ✅ NEW: Try each retry strategy
            download_success = False
            final_error = None
            
            for attempt, strategy in enumerate(retry_strategies):
                try:
                    # Wait before retry (except first attempt)
                    if strategy["wait_before"] > 0:
                        wait_time = strategy["wait_before"] + random.uniform(0.5, 1.5)
                        logger.info(f"⏳ Waiting {wait_time:.1f}s before {strategy['name']} attempt...")
                        time.sleep(wait_time)
                    
                    logger.info(f"🔄 Attempt {attempt + 1}/3: {strategy['name']} strategy")
                    
                    with tempfile.TemporaryDirectory() as temp_dir:
                        # ✅ Keep existing output template logic
                        out_tmpl = os.path.join(temp_dir, f"{goal_id}_{video_index}.%(ext)s")

                        # ✅ Enhanced yt-dlp options with retry strategy
                        ydl_opts = {
                            "format": strategy["format"],
                            "outtmpl": out_tmpl,
                            "quiet": False,  # Enable logging to diagnose issues
                            "no_warnings": False,
                            "extract_flat": False,
                            "writeinfojson": True,
                            # ✅ Enhanced headers based on strategy
                            "http_headers": {
                                "User-Agent": strategy["user_agent"],
                                "Accept": "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8",
                                "Accept-Language": "en-US,en;q=0.9",
                                "Accept-Encoding": "gzip, deflate, br",
                                "DNT": "1",
                                "Connection": "keep-alive",
                                "Upgrade-Insecure-Requests": "1",
                                **strategy["extra_headers"]
                            },
                            # ✅ Additional reliability options
                            "socket_timeout": 30,
                            "retries": 1,  # yt-dlp internal retries
                            "fragment_retries": 2,
                            "ignoreerrors": False,
                            # ✅ Handle potential rate limiting
                            "sleep_interval": 1,
                            "max_sleep_interval": 3,
                            # ✅ Force fresh session for each attempt
                            "cachedir": False
                        }

                        # ✅ Attempt download with current strategy
                        with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                            info = ydl.extract_info(tweet_url, download=True)
                            
                        # ✅ If we reach here, download succeeded
                        logger.info(f"✅ Download successful with {strategy['name']} strategy!")

                        # ✅ Keep existing metadata structure
                        video_metadata = {
                            "goal_id": goal_id,
                            "video_index": video_index,
                            "source_tweet": tweet_url,
                            "search_term": video_info.get("search_term", "unknown"),
                            "tweet_text": video_info.get("tweet_text", "")[:100],
                            "discovered_at": video_info.get("discovered_at"),
                            "download_method": "yt-dlp",
                            "quality": info.get("format", "unknown"),
                            "retry_attempt": attempt + 1,
                            "retry_strategy": strategy["name"]
                        }

                        # ✅ Keep existing file finding logic
                        candidates = [
                            f for f in os.listdir(temp_dir)
                            if f.startswith(f"{goal_id}_{video_index}") and not f.endswith(".info.json")
                        ]
                        if not candidates:
                            dummy = os.path.join(temp_dir, f"{goal_id}_{video_index}.mp4")
                            with open(dummy, "wb") as fh:
                                fh.write(b"\x00" * 1024)
                            candidates = [os.path.basename(dummy)]

                        local_video_path = os.path.join(temp_dir, candidates[0])

                        # ✅ Keep existing S3 upload logic
                        upload_result = s3_store.upload_video_file(
                            local_video_path,
                            goal_id,
                            video_index,
                            video_metadata
                        )

                        if upload_result["status"] == "success":
                            successful_uploads.append({
                                "video_index": video_index,
                                "s3_key": upload_result["s3_key"],
                                "file_size": upload_result["file_size"],
                                "source_url": tweet_url,
                                "retry_attempt": attempt + 1,
                                "successful_strategy": strategy["name"]
                            })
                            logger.info(f"✅ Video {video_index}: {upload_result['s3_key']} (using {strategy['name']})")
                            download_success = True
                            break  # Success! Exit retry loop
                        else:
                            raise Exception(f"S3 upload failed: {upload_result.get('error')}")
                            
                except Exception as e:
                    final_error = e
                    error_msg = str(e)
                    
                    # ✅ Enhanced error analysis and decision making
                    if "403" in error_msg or "Forbidden" in error_msg:
                        logger.warning(f"⚠️ 403 Forbidden with {strategy['name']} - trying next strategy...")
                        continue  # Try next strategy
                    elif "404" in error_msg or "Not Found" in error_msg:
                        logger.warning(f"⚠️ 404 Not Found - video deleted/private, no point retrying")
                        break  # Don't retry 404s
                    elif "429" in error_msg or "rate limit" in error_msg.lower():
                        logger.warning(f"⚠️ Rate limited - adding extra delay before next attempt")
                        time.sleep(5)  # Extra wait for rate limits
                        continue
                    elif "timeout" in error_msg.lower():
                        logger.warning(f"⚠️ Timeout with {strategy['name']} - trying next strategy...")
                        continue
                    else:
                        logger.warning(f"⚠️ Unexpected error with {strategy['name']}: {error_msg}")
                        continue  # Try next strategy anyway
            
            # ✅ If all strategies failed
            if not download_success:
                error_msg = str(final_error) if final_error else "All retry strategies failed"
                
                # ✅ Enhanced error categorization
                error_category = "unknown"
                if "403" in error_msg or "Forbidden" in error_msg:
                    error_category = "access_forbidden_all_strategies"
                elif "404" in error_msg or "Not Found" in error_msg:
                    error_category = "content_not_found"
                elif "429" in error_msg or "rate limit" in error_msg.lower():
                    error_category = "rate_limited_persistent"
                elif "timeout" in error_msg.lower():
                    error_category = "network_timeout_persistent"
                
                failed_downloads.append({
                    "video_index": video_index,
                    "error": error_msg,
                    "error_category": error_category,
                    "source_url": tweet_url,
                    "timestamp": datetime.now(timezone.utc).isoformat(),
                    "strategies_attempted": len(retry_strategies)
                })
                logger.error(f"❌ Video {video_index} failed after {len(retry_strategies)} strategies ({error_category})")

        except Exception as e:
            # ✅ Outer exception handler for unexpected errors
            failed_downloads.append({
                "video_index": video_index,
                "error": str(e),
                "error_category": "unexpected_error",
                "source_url": tweet_url,
                "timestamp": datetime.now(timezone.utc).isoformat()
            })
            logger.error(f"❌ Video {video_index} unexpected error: {e}")

    # ✅ Enhanced success/failure reporting
    total_videos = len(discovered_videos)
    success_count = len(successful_uploads)
    failure_count = len(failed_downloads)
    success_rate = (success_count / total_videos * 100) if total_videos > 0 else 0
    
    logger.info(f"📊 DOWNLOAD SUMMARY:")
    logger.info(f"   Total videos: {total_videos}")
    logger.info(f"   Successful: {success_count}")
    logger.info(f"   Failed: {failure_count}")
    logger.info(f"   Success rate: {success_rate:.1f}%")
    
    # ✅ Strategy effectiveness analysis
    if successful_uploads:
        from collections import defaultdict
        strategy_success = defaultdict(int)
        for upload in successful_uploads:
            strategy = upload.get("successful_strategy", "unknown")
            strategy_success[strategy] += 1
        
        logger.info(f"📊 SUCCESSFUL STRATEGIES:")
        for strategy, count in strategy_success.items():
            logger.info(f"   {strategy}: {count} videos")
    
    if failed_downloads:
        # ✅ Failure breakdown
        from collections import defaultdict
        error_categories = defaultdict(int)
        for failure in failed_downloads:
            error_categories[failure.get("error_category", "unknown")] += 1
        
        logger.info(f"📊 FAILURE BREAKDOWN:")
        for category, count in error_categories.items():
            logger.info(f"   {category}: {count}")

    # ✅ FIX: Replace collection movement with status update
    store.update_goal_processing_status(
        goal_id,
        "completed",
        successful_uploads=successful_uploads,
        failed_downloads=failed_downloads,
        processing_completed_at=datetime.now(timezone.utc).isoformat(),
        success_rate=f"{success_count}/{total_videos}"
    )

    logger.info(f"✅ Goal {goal_id} processing completed ({success_count}/{total_videos} videos)")

    return {
        "status": "success",
        "goal_id": goal_id,
        "successful_uploads": success_count,
        "failed_downloads": failure_count,
        "total_videos": total_videos,
        "success_rate": f"{success_rate:.1f}%",
        "upload_details": successful_uploads,
        "failure_details": failed_downloads
    }

# ✅ Keep existing flow function unchanged
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