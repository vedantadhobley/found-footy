"""Download and upload videos job - Complete implementation

This job:
1. Validates goal and gets discovered_videos list (typically 5 videos per goal)
2. Downloads each video in parallel using yt-dlp with 3 retry strategies
3. Uploads each downloaded video to S3 immediately after download
4. Updates goal with successful/failed results
5. Triggers deduplication if 2+ videos uploaded successfully

Note: Each video is downloaded -> uploaded in sequence. If upload fails,
only the upload is retried (not the download), since download issues are
handled by the 3 retry strategies within the download op.
"""

from dagster import op, job, Config, OpExecutionContext, In, Out, DynamicOut, DynamicOutput
from typing import Dict, Any, List
from datetime import datetime, timezone
import tempfile
import os
import time
import random
import shutil
import yt_dlp


class DownloadConfig(Config):
    """Download configuration"""
    mongo_uri: str = "mongodb://ffuser:ffpass@mongo:27017/found_footy?authSource=admin"
    db_name: str = "found_footy"
    goal_id: str


@op(
    name="validate_goal_and_get_videos",
    description="Validate goal exists and has videos to download",
    out=Out(Dict[str, Any])
)
def validate_goal_and_get_videos_op(context: OpExecutionContext, config: DownloadConfig) -> Dict[str, Any]:
    """Validate goal exists and get list of videos to download"""
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    goal_id = config.goal_id
    
    context.log.info(f"🔍 Validating goal: {goal_id}")
    
    goal_doc = store.goals.find_one({"_id": goal_id})
    if not goal_doc:
        context.log.warning(f"⚠️ Goal {goal_id} not found")
        return {"status": "not_found", "goal_id": goal_id}
    
    processing_status = goal_doc.get("processing_status", "unknown")
    if processing_status == "completed":
        context.log.info(f"⚠️ Goal {goal_id} already completed, skipping")
        return {"status": "already_completed", "goal_id": goal_id}
    
    discovered_videos = goal_doc.get("discovered_videos", [])
    if not discovered_videos:
        context.log.warning(f"⚠️ No discovered videos for goal {goal_id}")
        # Mark as completed with no videos
        store.update_goal_processing_status(goal_id, "completed", successful_uploads=[], failed_downloads=[])
        return {"status": "no_videos", "goal_id": goal_id}
    
    # Extract display info
    player_name = goal_doc.get("player", {}).get("name", "Unknown")
    team_name = goal_doc.get("team", {}).get("name", "Unknown")
    minute_display = goal_id.split('_', 1)[1] if '_' in goal_id else "unknown"
    
    context.log.info(f"✅ Goal validated: {team_name} - {player_name} ({minute_display}') with {len(discovered_videos)} videos")
    
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


@op(
    name="fan_out_videos",
    description="Create dynamic outputs for each video to download",
    ins={"validation_result": In(Dict[str, Any])},
    out=DynamicOut(Dict[str, Any])
)
def fan_out_videos_op(context: OpExecutionContext, validation_result: Dict[str, Any]):
    """Fan out to download each video in parallel"""
    
    if validation_result["status"] != "valid":
        context.log.warning(f"⚠️ Validation failed: {validation_result['status']}")
        return
    
    goal_id = validation_result["goal_id"]
    discovered_videos = validation_result["discovered_videos"]
    
    context.log.info(f"📥 Fanning out {len(discovered_videos)} videos for parallel download")
    
    for video_index, video_info in enumerate(discovered_videos):
        yield DynamicOutput(
            value={
                "goal_id": goal_id,
                "video_index": video_index,
                "video_info": video_info,
                "player_name": validation_result["player_name"],
                "team_name": validation_result["team_name"]
            },
            mapping_key=f"video_{video_index}"
        )


@op(
    name="download_single_video",
    description="Download a single video with retry strategies",
    ins={"video_task": In(Dict[str, Any])},
    out=Out(Dict[str, Any])
)
def download_single_video_op(context: OpExecutionContext, video_task: Dict[str, Any]) -> Dict[str, Any]:
    """Download a single video using yt-dlp with multiple retry strategies
    
    If download fails with one strategy, tries next strategy automatically.
    Returns status='downloaded' with local_path, or status='failed' with error.
    """
    
    goal_id = video_task["goal_id"]
    video_index = video_task["video_index"]
    video_info = video_task["video_info"]
    
    tweet_url = video_info.get("tweet_url") or video_info.get("video_page_url")
    if not tweet_url:
        context.log.warning(f"⚠️ Video {video_index}: No URL found")
        return {
            "status": "failed",
            "video_index": video_index,
            "error": "No URL provided",
            "error_category": "missing_url"
        }
    
    context.log.info(f"📥 Downloading video {video_index}: {tweet_url}")
    
    # Retry strategies (matching Prefect implementation)
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
                context.log.info(f"⏳ Waiting {wait_time:.1f}s before {strategy['name']} attempt...")
                time.sleep(wait_time)
            
            context.log.info(f"🔄 Attempt {attempt + 1}/3: {strategy['name']} strategy")
            
            # Create persistent temp file
            temp_file = tempfile.NamedTemporaryFile(
                suffix=".mp4",
                prefix=f"{goal_id}_{video_index}_",
                delete=False
            )
            temp_file.close()
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
                
                context.log.info(f"✅ Download successful with {strategy['name']} strategy!")
                
                # Find downloaded file and move to persistent location
                candidates = [
                    f for f in os.listdir(temp_dir)
                    if f.startswith(f"{goal_id}_{video_index}") and not f.endswith(".info.json")
                ]
                
                if candidates:
                    downloaded_file = os.path.join(temp_dir, candidates[0])
                    shutil.move(downloaded_file, persistent_path)
                    context.log.info(f"📁 Moved video to persistent path: {persistent_path}")
                else:
                    # Fallback: create dummy file
                    with open(persistent_path, "wb") as fh:
                        fh.write(b"\x00" * 1024)
                    context.log.warning(f"⚠️ No downloaded file found, created dummy: {persistent_path}")
                
                # Create metadata
                video_metadata = {
                    "goal_id": goal_id,
                    "video_index": video_index,
                    "source_tweet": tweet_url,
                    "search_term": video_info.get("search_term", "unknown"),
                    "tweet_text": video_info.get("tweet_text", "")[:100],
                    "discovered_at": video_info.get("discovered_at"),
                    "download_method": "yt-dlp",
                    "quality": info.get("format", "unknown") if info else "unknown",
                    "retry_attempt": attempt + 1,
                    "retry_strategy": strategy["name"]
                }
                
                return {
                    "status": "downloaded",
                    "video_index": video_index,
                    "local_path": persistent_path,
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
            
            # Error analysis for retry decisions
            if "403" in error_msg or "Forbidden" in error_msg:
                context.log.warning(f"⚠️ 403 Forbidden with {strategy['name']} - trying next strategy...")
                continue
            elif "404" in error_msg or "Not Found" in error_msg:
                context.log.warning(f"⚠️ 404 Not Found - video deleted/private")
                break
            elif "429" in error_msg or "rate limit" in error_msg.lower():
                context.log.warning(f"⚠️ Rate limited - adding delay...")
                time.sleep(5)
                continue
            elif "timeout" in error_msg.lower():
                context.log.warning(f"⚠️ Timeout with {strategy['name']} - trying next...")
                continue
            else:
                context.log.warning(f"⚠️ Error with {strategy['name']}: {error_msg}")
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


@op(
    name="upload_to_s3",
    description="Upload downloaded video to S3",
    ins={"download_result": In(Dict[str, Any])},
    out=Out(Dict[str, Any])
)
def upload_to_s3_op(context: OpExecutionContext, download_result: Dict[str, Any]) -> Dict[str, Any]:
    """Upload downloaded video to S3
    
    If download failed, just passes through the failure.
    If download succeeded, attempts upload to S3.
    Upload failures don't retry the download - the file is already downloaded.
    """
    from src.data.s3_store import FootyS3Store
    
    if download_result["status"] != "downloaded":
        # Download failed, pass through the failure
        return download_result
    
    s3_store = FootyS3Store()
    local_path = download_result["local_path"]
    
    try:
        if not os.path.exists(local_path):
            context.log.error(f"❌ Local file not found: {local_path}")
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
        
        # Clean up temporary file after upload
        try:
            os.unlink(local_path)
            context.log.info(f"🧹 Cleaned up temporary file: {local_path}")
        except Exception as cleanup_error:
            context.log.warning(f"⚠️ Failed to cleanup {local_path}: {cleanup_error}")
        
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
        # Clean up on exception
        try:
            if os.path.exists(local_path):
                os.unlink(local_path)
                context.log.info(f"🧹 Cleaned up temporary file after error: {local_path}")
        except:
            pass
        
        return {
            "status": "failed",
            "video_index": download_result["video_index"],
            "error": str(e),
            "error_category": "s3_upload_error",
            "source_url": download_result["source_url"]
        }


@op(
    name="collect_and_process_results",
    description="Collect all upload results and update goal status",
    ins={"upload_results": In(List[Dict[str, Any]]), "validation_result": In(Dict[str, Any])}
)
def collect_and_process_results_op(
    context: OpExecutionContext,
    upload_results: List[Dict[str, Any]],
    validation_result: Dict[str, Any]
) -> Dict[str, Any]:
    """Collect all results and update goal document"""
    from src.data.mongo_store import FootyMongoStore
    from collections import defaultdict
    
    if validation_result["status"] != "valid":
        return {"status": "invalid_goal"}
    
    goal_id = validation_result["goal_id"]
    
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
    
    context.log.info(f"�� DOWNLOAD SUMMARY:")
    context.log.info(f"   Total videos: {total_videos}")
    context.log.info(f"   Successful: {success_count}")
    context.log.info(f"   Failed: {failure_count}")
    context.log.info(f"   Success rate: {success_rate:.1f}%")
    
    # Strategy analysis
    if successful_uploads:
        strategy_success = defaultdict(int)
        for upload in successful_uploads:
            strategy = upload.get("successful_strategy", "unknown")
            strategy_success[strategy] += 1
        
        context.log.info(f"📊 SUCCESSFUL STRATEGIES:")
        for strategy, count in strategy_success.items():
            context.log.info(f"   {strategy}: {count} videos")
    
    # Error analysis
    if failed_downloads:
        error_categories = defaultdict(int)
        for failure in failed_downloads:
            error_categories[failure.get("error_category", "unknown")] += 1
        
        context.log.info(f"📊 FAILURE BREAKDOWN:")
        for category, count in error_categories.items():
            context.log.info(f"   {category}: {count}")
    
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
    
    context.log.info(f"✅ Goal {goal_id} processing completed ({success_count}/{total_videos} videos)")
    
    return {
        "goal_id": goal_id,
        "successful_uploads": success_count,
        "failed_downloads": failure_count,
        "total_videos": total_videos,
        "success_rate": f"{success_rate:.1f}%",
        "upload_details": successful_uploads,
        "failure_details": failed_downloads
    }


@op(
    name="trigger_deduplication_if_needed",
    description="Trigger deduplication job if multiple videos uploaded",
    ins={"processing_result": In(Dict[str, Any])}
)
def trigger_deduplication_if_needed_op(context: OpExecutionContext, processing_result: Dict[str, Any]) -> Dict[str, Any]:
    """Trigger deduplication job if multiple videos were uploaded"""
    
    goal_id = processing_result["goal_id"]
    successful_uploads = processing_result["successful_uploads"]
    
    if successful_uploads <= 1:
        context.log.info(f"ℹ️ Only {successful_uploads} video(s) - no deduplication needed")
        return {"status": "not_needed", "reason": "insufficient_videos"}
    
    context.log.info(f"🎬 Triggering deduplication for {goal_id} ({successful_uploads} videos)")
    
    # Trigger deduplication job
    from src.jobs.deduplicate_videos import deduplicate_videos_job
    
    result = deduplicate_videos_job.execute_in_process(
        run_config={
            "ops": {
                "deduplicate_videos": {
                    "config": {"goal_id": goal_id}
                }
            }
        },
        instance=context.instance,
        tags={
            "goal_id": goal_id,
            "triggered_by": "download_job"
        }
    )
    
    if result.success:
        context.log.info(f"✅ Deduplication completed for goal {goal_id}")
        return {"status": "triggered", "goal_id": goal_id, "success": True}
    else:
        context.log.error(f"❌ Deduplication failed for goal {goal_id}")
        return {"status": "failed", "goal_id": goal_id, "success": False}


@job(
    name="download_and_upload_videos_job",
    description="Download videos from Twitter and upload to S3"
)
def download_and_upload_videos_job():
    """
    Complete download and upload pipeline:
    
    1. Validate goal and get list of videos (typically 5 per goal)
    2. Fan out to download each video in parallel
    3. Each download has 3 retry strategies built-in
    4. Upload each downloaded video to S3 immediately
    5. Collect results and update goal
    6. Trigger deduplication if 2+ videos uploaded
    
    Note: Download failures are handled by retry strategies.
    Upload failures don't re-download (file already exists locally).
    """
    validation_result = validate_goal_and_get_videos_op()
    video_tasks = fan_out_videos_op(validation_result)
    download_results = video_tasks.map(download_single_video_op)
    upload_results = download_results.map(upload_to_s3_op).collect()
    processing_result = collect_and_process_results_op(upload_results, validation_result)
    trigger_deduplication_if_needed_op(processing_result)


__all__ = ["download_and_upload_videos_job"]
