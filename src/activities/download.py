"""Download activities - Granular per-video operations with individual retry"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import tempfile
import hashlib
import yt_dlp


@activity.defn
async def fetch_event_data(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Fetch event from fixtures_active and return discovered videos.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
    
    Returns:
        Dict with discovered_videos, player_name, team_name, event
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Get fixture
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        activity.logger.error(f"❌ Fixture {fixture_id} not found")
        return {"status": "error", "error": "fixture_not_found"}
    
    # Find event
    event = None
    for evt in fixture.get("events", []):
        if evt.get("_event_id") == event_id:
            event = evt
            break
    
    if not event:
        activity.logger.error(f"❌ Event {event_id} not found")
        return {"status": "error", "error": "event_not_found"}
    
    discovered_videos = event.get("_discovered_videos", [])
    if not discovered_videos:
        activity.logger.warning(f"⚠️ No videos to download for {event_id}")
        return {"status": "no_videos", "discovered_videos": []}
    
    player_name = event.get("player", {}).get("name", "Unknown")
    team_name = event.get("team", {}).get("name", "Unknown")
    
    activity.logger.info(f"📥 Found {len(discovered_videos)} videos for {event_id}")
    
    return {
        "status": "success",
        "discovered_videos": discovered_videos,
        "player_name": player_name,
        "team_name": team_name,
        "event": event,
    }


@activity.defn
async def download_single_video(video_url: str, video_index: int, event_id: str, temp_dir: str) -> Dict[str, Any]:
    """
    Download a single video with yt-dlp and calculate its hash.
    
    Args:
        video_url: URL to download (tweet URL or video page URL)
        video_index: Index of this video in the list
        event_id: Event ID for filename
        temp_dir: Temporary directory to download to
    
    Returns:
        Dict with file_path, file_hash, file_size, video_index
    """
    try:
        # Ensure temp directory exists (activity can do I/O)
        os.makedirs(temp_dir, exist_ok=True)
        
        output_path = os.path.join(temp_dir, f"{event_id}_{video_index}.mp4")
        
        activity.logger.info(f"📥 Downloading video {video_index}: {video_url[:50]}...")
        
        # Download with yt-dlp
        ydl_opts = {
            "format": "best[ext=mp4]/best",
            "outtmpl": output_path,
            "quiet": True,
            "no_warnings": True,
        }
        
        with yt_dlp.YoutubeDL(ydl_opts) as ydl:
            ydl.download([video_url])
        
        if not os.path.exists(output_path):
            activity.logger.error(f"❌ Download failed for video {video_index}")
            return {"status": "failed", "video_index": video_index}
        
        # Calculate hash and size
        file_hash = _calculate_md5(output_path)
        file_size = os.path.getsize(output_path)
        
        activity.logger.info(
            f"✅ Downloaded video {video_index}: "
            f"{os.path.basename(output_path)} ({file_size / 1024 / 1024:.2f} MB, hash: {file_hash[:8]}...)"
        )
        
        return {
            "status": "success",
            "file_path": output_path,
            "file_hash": file_hash,
            "file_size": file_size,
            "video_index": video_index,
        }
    
    except Exception as e:
        activity.logger.error(f"❌ Error downloading video {video_index}: {e}")
        return {"status": "failed", "video_index": video_index, "error": str(e)}


@activity.defn
async def deduplicate_videos(downloaded_files: List[Dict[str, Any]]) -> List[Dict[str, Any]]:
    """
    Deduplicate videos by hash, keeping largest file per hash.
    
    Args:
        downloaded_files: List of download results with file_path, file_hash, file_size
    
    Returns:
        List of unique videos (duplicates removed from disk)
    """
    # Filter out failed downloads
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    
    if not successful:
        activity.logger.warning("⚠️ No successful downloads to deduplicate")
        return []
    
    seen_hashes = {}  # hash -> file_info
    duplicates_removed = 0
    
    for file_info in successful:
        file_hash = file_info["file_hash"]
        file_path = file_info["file_path"]
        file_size = file_info["file_size"]
        
        if file_hash in seen_hashes:
            # Duplicate found
            existing = seen_hashes[file_hash]
            
            if file_size > existing["file_size"]:
                # This one is larger - delete existing, keep this
                activity.logger.info(
                    f"🔄 Duplicate: keeping larger file "
                    f"({file_size} > {existing['file_size']}, hash: {file_hash[:8]}...)"
                )
                os.remove(existing["file_path"])
                seen_hashes[file_hash] = file_info
            else:
                # Existing is larger - delete this one
                activity.logger.info(
                    f"🔄 Duplicate: keeping existing file "
                    f"({existing['file_size']} >= {file_size}, hash: {file_hash[:8]}...)"
                )
                os.remove(file_path)
            
            duplicates_removed += 1
        else:
            # Unique file
            seen_hashes[file_hash] = file_info
    
    unique_videos = list(seen_hashes.values())
    
    activity.logger.info(
        f"✅ Deduplication complete: {len(unique_videos)} unique videos "
        f"({duplicates_removed} duplicates removed)"
    )
    
    return unique_videos


@activity.defn
async def upload_single_video(
    file_path: str,
    fixture_id: int,
    event_id: str,
    player_name: str,
    team_name: str,
    video_index: int,
) -> Dict[str, Any]:
    """
    Upload a single video to S3 with metadata.
    
    Args:
        file_path: Local path to video file
        fixture_id: Fixture ID
        event_id: Event ID
        player_name: Player name for metadata
        team_name: Team name for metadata
        video_index: Index for logging
    
    Returns:
        Dict with s3_url and status
    """
    from src.data.s3_store import FootyS3Store
    
    try:
        filename = os.path.basename(file_path)
        
        # S3 key: {fixture_id}/{event_id}/{filename}
        s3_key = f"{fixture_id}/{event_id}/{filename}"
        
        # Metadata tags
        metadata = {
            "player_name": player_name,
            "team_name": team_name,
            "event_id": event_id,
            "fixture_id": str(fixture_id),
        }
        
        activity.logger.info(f"☁️ Uploading video {video_index} to S3: {s3_key}")
        
        s3_store = FootyS3Store()
        s3_url = s3_store.upload_video(file_path, s3_key, metadata=metadata)
        
        if s3_url:
            activity.logger.info(f"✅ Uploaded video {video_index}: {s3_url}")
            return {"status": "success", "s3_url": s3_url}
        else:
            activity.logger.error(f"❌ Upload failed for video {video_index}")
            return {"status": "failed", "video_index": video_index}
    
    except Exception as e:
        activity.logger.error(f"❌ Error uploading video {video_index}: {e}")
        return {"status": "failed", "video_index": video_index, "error": str(e)}


@activity.defn
async def mark_download_complete(
    fixture_id: int,
    event_id: str,
    s3_urls: List[str],
    temp_dir: str,
) -> bool:
    """
    Mark event as download complete and cleanup temp directory.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        s3_urls: List of S3 URLs that were uploaded
        temp_dir: Temporary directory to cleanup
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    import shutil
    
    store = FootyMongoStore()
    
    # Mark event as download complete
    success = store.mark_event_download_complete(fixture_id, event_id, s3_urls)
    
    if success:
        activity.logger.info(f"✅ Marked {event_id} as download complete with {len(s3_urls)} videos")
    else:
        activity.logger.warning(f"⚠️ Failed to update event {event_id}")
    
    # Cleanup temp directory
    if temp_dir and os.path.exists(temp_dir):
        shutil.rmtree(temp_dir)
        activity.logger.info(f"🧹 Cleaned up temp directory: {temp_dir}")
    
    return success


def _calculate_md5(file_path: str) -> str:
    """Calculate MD5 hash of file for deduplication"""
    hash_md5 = hashlib.md5()
    with open(file_path, "rb") as f:
        for chunk in iter(lambda: f.read(4096), b""):
            hash_md5.update(chunk)
    return hash_md5.hexdigest()
