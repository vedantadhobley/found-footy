"""Download Job - Download, deduplicate, and upload event videos

Clean implementation for fixture/event architecture.
S3 structure: {fixture_id}/{event_id}/{video_filename}
Deduplication happens in /tmp before upload.
"""
from dagster import Config, job, op, OpExecutionContext
import os
import tempfile
import hashlib
from pathlib import Path
from typing import Dict, Any, List
import yt_dlp

from src.data.mongo_store import FootyMongoStore


class DownloadJobConfig(Config):
    """Configuration for download job"""
    fixture_id: int
    event_id: str


@op(
    name="fetch_event_videos",
    description="Get event data and discovered videos from fixtures_active",
    tags={"kind": "mongodb", "stage": "fetch"}
)
def fetch_event_videos_op(
    context: OpExecutionContext,
    config: DownloadJobConfig
) -> Dict[str, Any]:
    """
    Fetch event from fixtures_active and return discovered videos.
    
    Returns:
        Dict with event_id, discovered_videos, player_name, team_name
    """
    fixture_id = config.fixture_id
    event_id = config.event_id
    
    store = FootyMongoStore()
    
    # Get fixture
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        context.log.error(f"❌ Fixture {fixture_id} not found")
        return {"status": "error", "error": "fixture_not_found"}
    
    # Find event
    event = None
    for evt in fixture.get("events", []):
        if evt.get("_event_id") == event_id:
            event = evt
            break
    
    if not event:
        context.log.error(f"❌ Event {event_id} not found")
        return {"status": "error", "error": "event_not_found"}
    
    discovered_videos = event.get("_discovered_videos", [])
    if not discovered_videos:
        context.log.warning(f"⚠️ No videos to download for {event_id}")
        return {"status": "no_videos", "event_id": event_id}
    
    # Extract metadata for S3 tags
    player_name = event.get("player", {}).get("name", "Unknown")
    team_name = event.get("team", {}).get("name", "Unknown")
    
    context.log.info(
        f"📥 Found {len(discovered_videos)} videos for {event_id} "
        f"({player_name} - {team_name})"
    )
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "event_id": event_id,
        "discovered_videos": discovered_videos,
        "player_name": player_name,
        "team_name": team_name,
        "event": event
    }


@op(
    name="download_and_deduplicate",
    description="Download videos to /tmp and deduplicate before upload",
    tags={"kind": "io", "stage": "download"}
)
def download_and_deduplicate_op(
    context: OpExecutionContext,
    fetch_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Download videos to temp directory and deduplicate based on file hash.
    Keeps only unique videos (best quality when duplicates exist).
    
    Returns:
        Dict with local_videos list (paths in /tmp)
    """
    if fetch_result.get("status") != "success":
        return fetch_result
    
    discovered_videos = fetch_result["discovered_videos"]
    event_id = fetch_result["event_id"]
    
    # Create temp directory
    temp_dir = tempfile.mkdtemp(prefix=f"footy_{event_id}_")
    context.log.info(f"📁 Created temp directory: {temp_dir}")
    
    downloaded_files = []
    seen_hashes = {}  # hash -> (file_path, file_size)
    
    for idx, video in enumerate(discovered_videos):
        try:
            video_url = video.get("tweet_url") or video.get("video_page_url")
            if not video_url:
                context.log.warning(f"⚠️ Video {idx}: No URL")
                continue
            
            context.log.info(f"📥 Downloading video {idx+1}/{len(discovered_videos)}")
            
            # Download with yt-dlp
            output_path = os.path.join(temp_dir, f"{event_id}_{idx}.mp4")
            
            ydl_opts = {
                "format": "best[ext=mp4]/best",
                "outtmpl": output_path,
                "quiet": True,
                "no_warnings": True,
            }
            
            with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                ydl.download([video_url])
            
            if not os.path.exists(output_path):
                context.log.warning(f"⚠️ Download failed for video {idx}")
                continue
            
            # Calculate file hash for deduplication
            file_hash = _calculate_file_hash(output_path)
            file_size = os.path.getsize(output_path)
            
            # Check for duplicate
            if file_hash in seen_hashes:
                existing_path, existing_size = seen_hashes[file_hash]
                context.log.info(
                    f"🔄 Duplicate detected (hash: {file_hash[:8]}...)"
                )
                # Keep larger file (better quality)
                if file_size > existing_size:
                    context.log.info(f"  → Keeping new file (larger: {file_size} > {existing_size})")
                    os.remove(existing_path)
                    downloaded_files.remove(existing_path)
                    seen_hashes[file_hash] = (output_path, file_size)
                    downloaded_files.append(output_path)
                else:
                    context.log.info(f"  → Keeping existing file (larger: {existing_size} >= {file_size})")
                    os.remove(output_path)
            else:
                # New unique video
                seen_hashes[file_hash] = (output_path, file_size)
                downloaded_files.append(output_path)
                context.log.info(f"  ✅ Downloaded: {os.path.basename(output_path)} ({file_size / 1024 / 1024:.2f} MB)")
        
        except Exception as e:
            context.log.error(f"❌ Error downloading video {idx}: {e}")
            continue
    
    unique_count = len(downloaded_files)
    total_count = len(discovered_videos)
    duplicates_removed = total_count - unique_count
    
    context.log.info(
        f"✅ Download complete: {unique_count} unique videos "
        f"({duplicates_removed} duplicates removed)"
    )
    
    return {
        **fetch_result,
        "local_videos": downloaded_files,
        "temp_dir": temp_dir,
        "unique_count": unique_count,
        "duplicates_removed": duplicates_removed
    }


@op(
    name="upload_to_s3_with_tags",
    description="Upload deduplicated videos to S3 with metadata tags",
    tags={"kind": "s3", "stage": "upload"}
)
def upload_to_s3_with_tags_op(
    context: OpExecutionContext,
    download_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Upload videos to S3 with structure: {fixture_id}/{event_id}/{filename}
    Adds metadata tags: player_name, team_name, event_id, fixture_id
    
    Returns:
        Dict with s3_urls list
    """
    if download_result.get("status") != "success":
        return download_result
    
    from src.data.s3_store import FootyS3Store
    
    local_videos = download_result.get("local_videos", [])
    if not local_videos:
        context.log.warning("⚠️ No videos to upload")
        return {**download_result, "s3_urls": []}
    
    fixture_id = download_result["fixture_id"]
    event_id = download_result["event_id"]
    player_name = download_result["player_name"]
    team_name = download_result["team_name"]
    
    s3_store = FootyS3Store()
    s3_urls = []
    
    for video_path in local_videos:
        try:
            filename = os.path.basename(video_path)
            
            # S3 key: {fixture_id}/{event_id}/{filename}
            s3_key = f"{fixture_id}/{event_id}/{filename}"
            
            # Metadata tags
            metadata = {
                "player_name": player_name,
                "team_name": team_name,
                "event_id": event_id,
                "fixture_id": str(fixture_id)
            }
            
            context.log.info(f"☁️ Uploading to S3: {s3_key}")
            
            # Upload with metadata
            s3_url = s3_store.upload_video(video_path, s3_key, metadata=metadata)
            
            if s3_url:
                s3_urls.append(s3_url)
                context.log.info(f"  ✅ Uploaded: {s3_url}")
            else:
                context.log.error(f"  ❌ Upload failed: {s3_key}")
        
        except Exception as e:
            context.log.error(f"❌ Error uploading {video_path}: {e}")
            continue
    
    # Cleanup temp directory
    temp_dir = download_result.get("temp_dir")
    if temp_dir and os.path.exists(temp_dir):
        import shutil
        shutil.rmtree(temp_dir)
        context.log.info(f"🧹 Cleaned up temp directory: {temp_dir}")
    
    context.log.info(f"✅ Uploaded {len(s3_urls)} videos to S3")
    
    return {
        **download_result,
        "s3_urls": s3_urls,
        "upload_count": len(s3_urls)
    }


@op(
    name="mark_download_complete",
    description="Update event in fixtures_active with S3 URLs",
    tags={"kind": "mongodb", "stage": "complete"}
)
def mark_download_complete_op(
    context: OpExecutionContext,
    upload_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Mark event as download complete and save S3 URLs.
    """
    if upload_result.get("status") != "success":
        return upload_result
    
    fixture_id = upload_result["fixture_id"]
    event_id = upload_result["event_id"]
    s3_urls = upload_result.get("s3_urls", [])
    
    store = FootyMongoStore()
    
    # Mark event as download complete
    success = store.mark_event_download_complete(fixture_id, event_id, s3_urls)
    
    if success:
        context.log.info(f"✅ Marked {event_id} as download complete with {len(s3_urls)} videos")
    else:
        context.log.warning(f"⚠️ Failed to update event {event_id}")
    
    return {
        **upload_result,
        "status": "completed"
    }


def _calculate_file_hash(file_path: str) -> str:
    """Calculate MD5 hash of file for deduplication"""
    hash_md5 = hashlib.md5()
    with open(file_path, "rb") as f:
        for chunk in iter(lambda: f.read(4096), b""):
            hash_md5.update(chunk)
    return hash_md5.hexdigest()


@job(
    name="download_job",
    description="Download, deduplicate, and upload event videos to S3",
    tags={"pipeline": "download", "trigger": "twitter", "phase": "processing"}
)
def download_job():
    """
    Complete video download pipeline.
    
    Flow:
    1. Fetch event and discovered_videos from fixtures_active
    2. Download videos to /tmp
    3. Deduplicate based on file hash (keep best quality)
    4. Upload unique videos to S3: {fixture_id}/{event_id}/{filename}
    5. Add S3 metadata tags: player_name, team_name, event_id, fixture_id
    6. Mark event as download_complete
    
    Config (fixture_id, event_id) provided at runtime via RunConfig.
    """
    fetch_result = fetch_event_videos_op()
    download_result = download_and_deduplicate_op(fetch_result)
    upload_result = upload_to_s3_with_tags_op(download_result)
    mark_download_complete_op(upload_result)
