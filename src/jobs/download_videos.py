"""Download videos job - downloads videos from Twitter and uploads to S3

Migrated from found_footy/flows/download_flow.py
"""

import os
import tempfile
import logging
from typing import Dict, Any, List

import yt_dlp
from dagster import job, op, OpExecutionContext, Config, DynamicOut, DynamicOutput

from src.data.mongo_store import FootyMongoStore
from src.data.s3_store import FootyS3Store

logger = logging.getLogger(__name__)


class DownloadVideosConfig(Config):
    """Configuration for downloading videos"""
    goal_id: str


@op(
    name="get_videos_to_download",
    description="Get list of videos to download from MongoDB",
    out=DynamicOut(Dict[str, Any])
)
def get_videos_to_download_op(
    context: OpExecutionContext,
    config: DownloadVideosConfig
):
    """Get discovered videos that need to be downloaded - yields each video as DynamicOutput"""
    goal_id = config.goal_id
    context.log.info(f"📥 Getting videos to download for goal: {goal_id}")
    
    store = FootyMongoStore()
    goal_doc = store.goals.find_one({"_id": goal_id})
    
    if not goal_doc:
        context.log.warning(f"Goal {goal_id} not found")
        return
    
    discovered_videos = goal_doc.get("discovered_videos", [])
    
    if not discovered_videos:
        context.log.warning(f"No videos to download for {goal_id}")
        store.update_goal_processing_status(goal_id, "completed", successful_uploads=[], failed_downloads=[])
        return
    
    context.log.info(f"Found {len(discovered_videos)} videos to download")
    
    for idx, video in enumerate(discovered_videos):
        yield DynamicOutput(
            value={
                "goal_id": goal_id,
                "video_index": idx,
                "video_info": video
            },
            mapping_key=f"video_{idx}"
        )


@op(
    name="download_and_upload_video",
    description="Download a single video from Twitter and upload to S3"
)
def download_and_upload_video_op(
    context: OpExecutionContext,
    video_data: Dict[str, Any]
) -> Dict[str, Any]:
    """Download video with yt-dlp and upload to S3"""
    goal_id = video_data["goal_id"]
    video_index = video_data["video_index"]
    video_info = video_data["video_info"]
    
    tweet_url = video_info.get("tweet_url") or video_info.get("video_page_url")
    
    if not tweet_url:
        context.log.warning(f"Video {video_index}: No URL found")
        return {
            "status": "failed",
            "goal_id": goal_id,
            "video_index": video_index,
            "error": "No URL"
        }
    
    context.log.info(f"📥 Downloading video {video_index}: {tweet_url}")
    
    # Download with yt-dlp
    try:
        with tempfile.TemporaryDirectory() as temp_dir:
            out_tmpl = os.path.join(temp_dir, f"{goal_id}_{video_index}.%(ext)s")
            
            ydl_opts = {
                "format": "best[height<=720]",
                "outtmpl": out_tmpl,
                "quiet": True,
                "no_warnings": True,
                "writeinfojson": True,
                "socket_timeout": 30,
            }
            
            with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                info = ydl.extract_info(tweet_url, download=True)
            
            # Find downloaded file
            candidates = [
                f for f in os.listdir(temp_dir)
                if f.startswith(f"{goal_id}_{video_index}") and not f.endswith(".info.json")
            ]
            
            if not candidates:
                context.log.error(f"No file downloaded for video {video_index}")
                return {
                    "status": "failed",
                    "goal_id": goal_id,
                    "video_index": video_index,
                    "error": "Download produced no file"
                }
            
            downloaded_file = os.path.join(temp_dir, candidates[0])
            
            # Upload to S3
            context.log.info(f"☁️ Uploading video {video_index} to S3...")
            s3_store = FootyS3Store()
            
            video_metadata = {
                "goal_id": goal_id,
                "video_index": video_index,
                "source_tweet": tweet_url,
                "search_term": video_info.get("search_term", "unknown"),
                "download_method": "yt-dlp",
                "quality": info.get("format", "unknown"),
            }
            
            upload_result = s3_store.upload_video_file(
                downloaded_file,
                goal_id,
                video_index,
                video_metadata
            )
            
            if upload_result["status"] == "success":
                context.log.info(f"✅ Video {video_index} uploaded successfully")
                return {
                    "status": "success",
                    "goal_id": goal_id,
                    "video_index": video_index,
                    "s3_key": upload_result["s3_key"],
                    "file_size": upload_result["file_size"]
                }
            else:
                context.log.error(f"❌ S3 upload failed for video {video_index}")
                return {
                    "status": "failed",
                    "goal_id": goal_id,
                    "video_index": video_index,
                    "error": "S3 upload failed"
                }
                
    except Exception as e:
        context.log.error(f"❌ Error processing video {video_index}: {e}")
        return {
            "status": "failed",
            "goal_id": goal_id,
            "video_index": video_index,
            "error": str(e)
        }


@op(
    name="update_goal_completion",
    description="Update goal with download results"
)
def update_goal_completion_op(
    context: OpExecutionContext,
    results: List[Dict[str, Any]]
):
    """Update goal processing status with download results"""
    # Extract goal_id from first result (all results should have same goal_id)
    if not results:
        context.log.warning("No results to process")
        return
    
    goal_id = results[0].get("goal_id")
    if not goal_id:
        # Try to get from video_index in results
        for r in results:
            if "goal_id" in r:
                goal_id = r["goal_id"]
                break
        
        if not goal_id:
            context.log.error("Could not determine goal_id from results")
            return
    
    successful = [r for r in results if r["status"] == "success"]
    failed = [r for r in results if r["status"] == "failed"]
    
    context.log.info(f"📊 Download complete for {goal_id}: {len(successful)} successful, {len(failed)} failed")
    
    store = FootyMongoStore()
    store.update_goal_processing_status(
        goal_id,
        "completed",
        successful_uploads=[r["s3_key"] for r in successful],
        failed_downloads=[r.get("error", "unknown") for r in failed]
    )
    
    context.log.info(f"✅ Goal {goal_id} processing completed")


@job(
    name="download_videos",
    description="Download goal videos from Twitter and upload to S3"
)
def download_videos_job():
    """Video download workflow"""
    videos = get_videos_to_download_op()
    results = videos.map(download_and_upload_video_op).collect()
    update_goal_completion_op(results)
