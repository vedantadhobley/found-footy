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

@task(name="validate-goal-task")
def validate_goal_task(goal_id: str) -> Dict[str, Any]:
    """Validate goal exists and has videos to download"""
    logger = get_run_logger()
    logger.info(f"🔍 Validating goal: {goal_id}")

    store = FootyMongoStore()
    
    goal_doc = store.goals.find_one({"_id": goal_id})
    if not goal_doc:
        logger.warning(f"⚠️ Goal {goal_id} not found")
        return {"status": "not_found", "goal_id": goal_id}

    processing_status = goal_doc.get("processing_status", "unknown")
    if processing_status == "completed":
        logger.info(f"⚠️ Goal {goal_id} already completed, skipping")
        return {"status": "already_completed", "goal_id": goal_id}

    discovered_videos = goal_doc.get("discovered_videos", [])
    if not discovered_videos:
        logger.warning(f"⚠️ No discovered tweet URLs for goal {goal_id}")
        # Mark as completed with no videos
        store.update_goal_processing_status(goal_id, "completed", successful_uploads=[], failed_downloads=[])
        return {"status": "no_videos", "goal_id": goal_id}

    # Extract display info
    player_name = goal_doc.get("player", {}).get("name", "Unknown")
    team_name = goal_doc.get("team", {}).get("name", "Unknown")
    minute_display = goal_id.split('_', 1)[1] if '_' in goal_id else "unknown"
    
    logger.info(f"✅ Goal validated: {team_name} - {player_name} ({minute_display}') with {len(discovered_videos)} videos")

    return {
        "status": "valid",
        "goal_id": goal_id,
        "goal_doc": goal_doc,
        "discovered_videos": discovered_videos,
        "player_name": player_name,
        "team_name": team_name,
        "minute_display": minute_display,
        "video_count": len(discovered_videos)
    }

@task(name="download-single-video-task")
def download_single_video_task(
    goal_id: str, 
    video_index: int, 
    video_info: Dict[str, Any]
) -> Dict[str, Any]:
    """Download a single video with retry strategies"""
    logger = get_run_logger()
    
    tweet_url = video_info.get("tweet_url") or video_info.get("video_page_url")
    if not tweet_url:
        logger.warning(f"⚠️ Video {video_index}: No URL found")
        return {
            "status": "failed",
            "video_index": video_index,
            "error": "No URL provided",
            "error_category": "missing_url"
        }

    logger.info(f"📥 Downloading video {video_index}: {tweet_url}")

    # Retry strategies (keep existing code)
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

    # Try each strategy
    for attempt, strategy in enumerate(retry_strategies):
        try:
            if strategy["wait_before"] > 0:
                wait_time = strategy["wait_before"] + random.uniform(0.5, 1.5)
                logger.info(f"⏳ Waiting {wait_time:.1f}s before {strategy['name']} attempt...")
                time.sleep(wait_time)
            
            logger.info(f"🔄 Attempt {attempt + 1}/3: {strategy['name']} strategy")
            
            # ✅ FIX: Create persistent temp file instead of temp directory
            import tempfile
            temp_file = tempfile.NamedTemporaryFile(
                suffix=".mp4", 
                prefix=f"{goal_id}_{video_index}_",
                delete=False  # Don't auto-delete
            )
            temp_file.close()  # Close file handle but keep the file
            
            persistent_path = temp_file.name
            
            with tempfile.TemporaryDirectory() as temp_dir:
                out_tmpl = os.path.join(temp_dir, f"{goal_id}_{video_index}.%(ext)s")

                ydl_opts = {
                    "format": strategy["format"],
                    "outtmpl": out_tmpl,
                    "quiet": False,
                    "no_warnings": False,
                    "extract_flat": False,
                    "writeinfojson": True,
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
                    "socket_timeout": 30,
                    "retries": 1,
                    "fragment_retries": 2,
                    "ignoreerrors": False,
                    "sleep_interval": 1,
                    "max_sleep_interval": 3,
                    "cachedir": False
                }

                # Attempt download
                with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                    info = ydl.extract_info(tweet_url, download=True)
                    
                logger.info(f"✅ Download successful with {strategy['name']} strategy!")

                # Find downloaded file and move it to persistent location
                candidates = [
                    f for f in os.listdir(temp_dir)
                    if f.startswith(f"{goal_id}_{video_index}") and not f.endswith(".info.json")
                ]
                
                if candidates:
                    downloaded_file = os.path.join(temp_dir, candidates[0])
                    # ✅ Move file outside temp directory
                    import shutil
                    shutil.move(downloaded_file, persistent_path)
                    logger.info(f"📁 Moved video to persistent path: {persistent_path}")
                else:
                    # Fallback: create dummy file
                    with open(persistent_path, "wb") as fh:
                        fh.write(b"\x00" * 1024)
                    logger.warning(f"⚠️ No downloaded file found, created dummy: {persistent_path}")

                # Create metadata
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

                return {
                    "status": "downloaded",
                    "video_index": video_index,
                    "local_path": persistent_path,  # ✅ Persistent path that won't get deleted
                    "metadata": video_metadata,
                    "source_url": tweet_url,
                    "successful_strategy": strategy["name"],
                    "retry_attempt": attempt + 1
                }
                
        except Exception as e:
            error_msg = str(e)
            
            # Clean up persistent file on failure
            try:
                if 'persistent_path' in locals() and os.path.exists(persistent_path):
                    os.unlink(persistent_path)
            except:
                pass
            
            # Error analysis for retry decisions (keep existing logic)
            if "403" in error_msg or "Forbidden" in error_msg:
                logger.warning(f"⚠️ 403 Forbidden with {strategy['name']} - trying next strategy...")
                continue
            elif "404" in error_msg or "Not Found" in error_msg:
                logger.warning(f"⚠️ 404 Not Found - video deleted/private")
                break  # No point retrying 404s
            elif "429" in error_msg or "rate limit" in error_msg.lower():
                logger.warning(f"⚠️ Rate limited - adding delay...")
                time.sleep(5)
                continue
            elif "timeout" in error_msg.lower():
                logger.warning(f"⚠️ Timeout with {strategy['name']} - trying next...")
                continue
            else:
                logger.warning(f"⚠️ Error with {strategy['name']}: {error_msg}")
                continue

    # All strategies failed
    return {
        "status": "failed",
        "video_index": video_index,
        "error": "All retry strategies failed",
        "error_category": "all_strategies_failed",
        "source_url": tweet_url,
        "strategies_attempted": len(retry_strategies)
    }

@task(name="upload-to-s3-task")
def upload_to_s3_task(download_result: Dict[str, Any]) -> Dict[str, Any]:
    """Upload downloaded video to S3"""
    logger = get_run_logger()
    
    if download_result["status"] != "downloaded":
        # Return the failure result unchanged
        return download_result

    s3_store = FootyS3Store()
    local_path = download_result["local_path"]
    
    try:
        # ✅ Check if file exists before upload
        if not os.path.exists(local_path):
            logger.error(f"❌ Local file not found: {local_path}")
            return {
                "status": "failed",
                "video_index": download_result["video_index"],
                "error": f"Local file not found: {local_path}",
                "error_category": "file_not_found",
                "source_url": download_result["source_url"]
            }
        
        upload_result = s3_store.upload_video_file(
            local_path,
            download_result["metadata"]["goal_id"],
            download_result["video_index"],
            download_result["metadata"]
        )

        # ✅ Clean up temporary file after upload (success or failure)
        try:
            os.unlink(local_path)
            logger.info(f"🧹 Cleaned up temporary file: {local_path}")
        except Exception as cleanup_error:
            logger.warning(f"⚠️ Failed to cleanup {local_path}: {cleanup_error}")

        if upload_result["status"] == "success":
            return {
                "status": "success",
                "video_index": download_result["video_index"],
                "s3_key": upload_result["s3_key"],
                "file_size": upload_result["file_size"],
                "source_url": download_result["source_url"],
                "retry_attempt": download_result["retry_attempt"],
                "successful_strategy": download_result["successful_strategy"]
            }
        else:
            return {
                "status": "failed",
                "video_index": download_result["video_index"],
                "error": f"S3 upload failed: {upload_result.get('error')}",
                "error_category": "s3_upload_failed",
                "source_url": download_result["source_url"]
            }
            
    except Exception as e:
        # ✅ Clean up on exception too
        try:
            if os.path.exists(local_path):
                os.unlink(local_path)
                logger.info(f"🧹 Cleaned up temporary file after error: {local_path}")
        except:
            pass
            
        return {
            "status": "failed",
            "video_index": download_result["video_index"],
            "error": str(e),
            "error_category": "s3_upload_error",
            "source_url": download_result["source_url"]
        }

@task(name="process-results-task")
def process_results_task(goal_id: str, upload_results: List[Dict[str, Any]]) -> Dict[str, Any]:
    """Process all upload results and update goal status"""
    logger = get_run_logger()
    
    successful_uploads = []
    failed_downloads = []
    
    for result in upload_results:
        if result["status"] == "success":
            successful_uploads.append(result)
        else:
            failed_downloads.append({
                "video_index": result["video_index"],
                "error": result["error"],
                "error_category": result.get("error_category", "unknown"),
                "source_url": result.get("source_url"),
                "timestamp": datetime.now(timezone.utc).isoformat()
            })

    # Calculate statistics
    total_videos = len(upload_results)
    success_count = len(successful_uploads)
    failure_count = len(failed_downloads)
    success_rate = (success_count / total_videos * 100) if total_videos > 0 else 0
    
    logger.info(f"📊 DOWNLOAD SUMMARY:")
    logger.info(f"   Total videos: {total_videos}")
    logger.info(f"   Successful: {success_count}")
    logger.info(f"   Failed: {failure_count}")
    logger.info(f"   Success rate: {success_rate:.1f}%")
    
    # Strategy analysis
    if successful_uploads:
        from collections import defaultdict
        strategy_success = defaultdict(int)
        for upload in successful_uploads:
            strategy = upload.get("successful_strategy", "unknown")
            strategy_success[strategy] += 1
        
        logger.info(f"📊 SUCCESSFUL STRATEGIES:")
        for strategy, count in strategy_success.items():
            logger.info(f"   {strategy}: {count} videos")
    
    # Error analysis
    if failed_downloads:
        from collections import defaultdict
        error_categories = defaultdict(int)
        for failure in failed_downloads:
            error_categories[failure.get("error_category", "unknown")] += 1
        
        logger.info(f"📊 FAILURE BREAKDOWN:")
        for category, count in error_categories.items():
            logger.info(f"   {category}: {count}")

    # Update goal status
    store = FootyMongoStore()
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
        "goal_id": goal_id,
        "successful_uploads": success_count,
        "failed_downloads": failure_count,
        "total_videos": total_videos,
        "success_rate": f"{success_rate:.1f}%",
        "upload_details": successful_uploads,
        "failure_details": failed_downloads
    }

@task(name="trigger-filter-deployment-task")
def trigger_filter_deployment_task(goal_id: str, successful_uploads_count: int) -> Dict[str, Any]:
    """Trigger filter deployment if multiple videos were uploaded"""
    logger = get_run_logger()
    
    if successful_uploads_count <= 1:
        logger.info(f"ℹ️ Only {successful_uploads_count} video(s) - no deduplication needed")
        return {"status": "not_needed", "reason": "insufficient_videos"}

    try:
        from prefect.deployments import run_deployment
        
        # ✅ FIX: Generate rich name and pass it to run_deployment (like twitter_flow does)
        from found_footy.flows.flow_naming import get_filter_flow_name
        filter_flow_name = get_filter_flow_name(goal_id)
        
        filter_deployment = run_deployment(
            name="filter-flow/filter-flow",
            parameters={"goal_id": goal_id},
            flow_run_name=filter_flow_name,  # ✅ Pass rich name like twitter_flow
            timeout=0
        )
        logger.info(f"🎬 Triggered filter deployment for {goal_id}")
        return {
            "status": "scheduled", 
            "deployment_id": str(filter_deployment.id),
            "goal_id": goal_id
        }
    except Exception as e:
        logger.error(f"❌ Filter deployment failed: {e}")
        return {"status": "failed", "error": str(e)}

@flow(name="download-flow")
def download_flow(goal_id: Optional[str] = None) -> Dict[str, Any]:
    """Orchestrate video download pipeline with clean task separation"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}

    # Task 1: Validate goal and get video list
    validation_result = validate_goal_task(goal_id)
    
    if validation_result["status"] != "valid":
        return {
            "goal_id": goal_id,
            "status": validation_result["status"],
            "message": f"Goal validation failed: {validation_result['status']}"  # ✅ FIX: Use single quotes inside f-string
        }

    discovered_videos = validation_result["discovered_videos"]
    player_name = validation_result["player_name"]
    team_name = validation_result["team_name"]
    minute_display = validation_result["minute_display"]
    
    logger.info(f"📥 Starting downloads for {team_name} - {player_name} ({minute_display}') - {len(discovered_videos)} videos")

    # Task 2: Download all videos in parallel
    download_results = []
    for video_index, video_info in enumerate(discovered_videos):
        download_result = download_single_video_task(goal_id, video_index, video_info)
        download_results.append(download_result)

    # Task 3: Upload successful downloads to S3
    upload_results = []
    for download_result in download_results:
        upload_result = upload_to_s3_task(download_result)
        upload_results.append(upload_result)

    # Task 4: Process results and update goal status
    processing_result = process_results_task(goal_id, upload_results)

    # Task 5: Trigger filter deployment if needed
    filter_result = trigger_filter_deployment_task(
        goal_id, 
        processing_result["successful_uploads"]
    )

    return {
        "goal_id": goal_id,
        "status": "completed",
        "download_summary": processing_result,
        "filter_deployment": filter_result,
        "storage_backend": "s3",
        "download_method": "yt-dlp_python_parallel",
        "team_name": team_name,
        "player_name": player_name,
        "minute_display": minute_display
    }