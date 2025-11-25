"""Download videos from URLs"""
import subprocess
import tempfile
from pathlib import Path
from typing import Any, Dict, List

from dagster import OpExecutionContext, op


@op(
    name="download_videos",
    description="Download videos from extracted URLs to local storage",
    tags={"kind": "io", "purpose": "download"}
)
def download_videos_op(
    context: OpExecutionContext,
    video_metadata: List[Dict[str, Any]]
) -> List[Dict[str, Any]]:
    """
    Download videos from URLs to temporary local storage.
    
    NOTE: Currently downloads to temp directory.
    Future: Consider dedicated local storage directory.
    
    Args:
        video_metadata: List of video metadata dicts with URLs
        
    Returns:
        List of downloaded video info with local paths
    """
    if not video_metadata:
        context.log.warning("⚠️  No videos to download")
        return []
    
    context.log.info(f"⬇️  Downloading {len(video_metadata)} videos")
    
    downloaded_videos = []
    
    # Create temp directory for downloads that persists for S3 upload
    temp_dir = tempfile.mkdtemp()
    temp_path = Path(temp_dir)
    
    for idx, video in enumerate(video_metadata):
        try:
            video_url = video.get("video_url")
            tweet_id = video.get("tweet_id", "unknown")
            
            if not video_url:
                context.log.warning(f"⚠️  Video metadata missing URL for tweet {tweet_id}")
                continue
            
            # Download with yt-dlp
            output_file = temp_path / f"{tweet_id}.mp4"
            
            context.log.info(f"   📥 Downloading video {idx+1}/{len(video_metadata)} from tweet {tweet_id}")
            
            cmd = [
                "yt-dlp",
                "-f", "best[ext=mp4]/best",  # Best quality MP4
                "-o", str(output_file),
                video_url
            ]
            
            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=120  # 2 minute timeout per video
            )
            
            if result.returncode == 0 and output_file.exists():
                # Get file stats
                file_size = output_file.stat().st_size
                
                downloaded_videos.append({
                    **video,
                    "local_path": str(output_file),
                    "file_size": file_size,
                    "downloaded": True
                })
                
                context.log.info(f"   ✅ Downloaded: {output_file.name} ({file_size / 1024 / 1024:.2f} MB)")
            else:
                context.log.error(f"   ❌ Download failed for tweet {tweet_id}: {result.stderr}")
            
        except subprocess.TimeoutExpired:
            context.log.error(f"❌ Download timeout for tweet {tweet_id}")
            continue
        except Exception as e:
            context.log.error(f"❌ Error downloading video from tweet {tweet_id}: {e}")
            continue
    
    context.log.info(f"✅ Downloaded {len(downloaded_videos)} videos to {temp_dir}")
    context.log.info("⚠️  Temp directory will persist until S3 upload completes")
    
    return downloaded_videos
