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
    Also checks S3 for existing videos to avoid re-downloading.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
    
    Returns:
        Dict with discovered_videos, player_name, team_name, event, existing_s3_hashes
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.s3_store import FootyS3Store
    
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
    assister_name = event.get("assist", {}).get("player", {}).get("name", "")
    
    # Get S3 URLs and perceptual hashes from MongoDB
    existing_s3_urls = event.get("_s3_urls", [])
    existing_perceptual_hashes = event.get("_perceptual_hashes", [])
    
    # Filter out videos we've already downloaded by checking URLs
    # Build set of video URLs we've already processed
    already_downloaded_urls = set()
    
    # Check S3 store for existing videos with source URLs in metadata
    # Each S3 object has metadata with the original tweet URL
    if existing_s3_urls:
        from src.data.s3_store import FootyS3Store
        s3_store = FootyS3Store()
        
        for s3_url in existing_s3_urls:
            try:
                # Extract bucket and key from S3 URL
                # Format: http://found-footy-minio:9000/footy-videos/1378993/event_id/file.mp4
                parts = s3_url.split("/footy-videos/", 1)
                if len(parts) == 2:
                    s3_key = parts[1]
                    # Use boto3's head_object to get metadata
                    response = s3_store.s3_client.head_object(Bucket="footy-videos", Key=s3_key)
                    metadata = response.get("Metadata", {})
                    source_url = metadata.get("source_url")
                    if source_url:
                        already_downloaded_urls.add(source_url)
                        activity.logger.debug(f"✓ Found source URL in S3 metadata: {source_url}")
                    else:
                        activity.logger.warning(f"⚠️ No source_url in metadata for {s3_url}")
            except Exception as e:
                # If we can't get metadata, log and skip (better to re-download than miss videos)
                activity.logger.warning(f"⚠️ Failed to get S3 metadata for {s3_url}: {e}")
    
    # Filter discovered_videos to only NEW ones
    videos_to_download = []
    for video in discovered_videos:
        video_url = video.get("tweet_url") or video.get("video_page_url")
        if video_url not in already_downloaded_urls:
            videos_to_download.append(video)
        else:
            activity.logger.debug(f"⏭️ Skipping already downloaded video: {video_url}")
    
    if not videos_to_download:
        activity.logger.info(f"⏭️ No new videos to download (all {len(discovered_videos)} already in S3)")
        return {
            "status": "no_videos",
            "discovered_videos": [],
            "event": event,
            "existing_s3_hashes": existing_perceptual_hashes
        }
    
    if existing_perceptual_hashes:
        activity.logger.info(f"🔍 Have {len(existing_perceptual_hashes)} existing perceptual hashes for cross-resolution deduplication")
    
    if len(videos_to_download) < len(discovered_videos):
        activity.logger.info(f"📥 Found {len(videos_to_download)} new videos to download ({len(discovered_videos) - len(videos_to_download)} already in S3)")
    else:
        activity.logger.info(f"📥 Found {len(videos_to_download)} videos to download for {event_id}")
    
    return {
        "status": "success",
        "discovered_videos": videos_to_download,
        "player_name": player_name,
        "team_name": team_name,
        "event": event,
        "existing_s3_hashes": existing_perceptual_hashes,  # Perceptual hashes for dedup
    }


@activity.defn
async def download_single_video(
    video_url: str, 
    video_index: int, 
    event_id: str, 
    temp_dir: str,
    source_tweet_url: str = ""
) -> Dict[str, Any]:
    """
    Download a single video with yt-dlp, extract metadata, and calculate hash.
    
    Args:
        video_url: URL to download (video page URL from Twitter)
        video_index: Index of this video in the list
        event_id: Event ID for filename
        temp_dir: Temporary directory to download to
        source_tweet_url: Original tweet URL (for dedup checking)
    
    Returns:
        Dict with file_path, file_hash, file_size, video_index, metadata (resolution, bitrate)
    
    Raises:
        Exception: If download fails (for Temporal retry)
    """
    # Ensure temp directory exists (activity can do I/O)
    os.makedirs(temp_dir, exist_ok=True)
    
    output_path = os.path.join(temp_dir, f"{event_id}_{video_index}.mp4")
    
    activity.logger.info(f"📥 Downloading video {video_index}: {video_url[:50]}...")
    
    # Download with yt-dlp and extract metadata
    # Use Twitter cookies for authentication (fixes premium video blocking)
    cookies_json_file = "/config/twitter_cookies.json"
    cookies_netscape_file = "/tmp/twitter_cookies_netscape.txt"
    
    ydl_opts = {
        "format": "best[ext=mp4]/best",
        "outtmpl": output_path,
        "quiet": True,
        "no_warnings": True,
    }
    
    # Convert JSON cookies to Netscape format (required by yt-dlp)
    if os.path.exists(cookies_json_file):
        try:
            _convert_cookies_to_netscape(cookies_json_file, cookies_netscape_file)
            ydl_opts["cookiefile"] = cookies_netscape_file
            activity.logger.debug(f"Using Twitter session cookies for authentication")
        except Exception as e:
            activity.logger.warning(f"⚠️ Could not load cookies: {e}")
    
    video_info = None
    with yt_dlp.YoutubeDL(ydl_opts) as ydl:
        # Extract info first to get metadata
        try:
            video_info = ydl.extract_info(video_url, download=True)
        except Exception as e:
            activity.logger.warning(f"⚠️ Could not extract metadata: {e}")
            # Try download without metadata
            ydl.download([video_url])
    
    if not os.path.exists(output_path):
        msg = f"❌ Download failed for video {video_index}: file does not exist after yt-dlp"
        activity.logger.error(msg)
        raise RuntimeError(msg)
    
    # Get video duration
    duration = _get_video_duration(output_path)
    
    # Duration filter: 5-60 seconds (typical goal clips)
    if duration < 5.0:
        activity.logger.warning(
            f"⏱️ Video {video_index} too short ({duration:.1f}s < 5s), skipping"
        )
        os.remove(output_path)
        return {"status": "filtered", "reason": "too_short", "duration": duration}
    
    if duration > 60.0:
        activity.logger.warning(
            f"⏱️ Video {video_index} too long ({duration:.1f}s > 60s), skipping"
        )
        os.remove(output_path)
        return {"status": "filtered", "reason": "too_long", "duration": duration}
    
    # Calculate MD5 hash and size first
    file_hash = _calculate_md5(output_path)
    file_size = os.path.getsize(output_path)
    
    # Generate perceptual hash (file_size + frames at 1s, 2s, 3s)
    perceptual_hash = _generate_perceptual_hash(output_path, file_size)
    
    # Extract quality metadata
    width = 0
    height = 0
    bitrate = 0
    fps = 0
    
    if video_info:
        width = video_info.get("width", 0)
        height = video_info.get("height", 0)
        bitrate = video_info.get("tbr", 0)  # total bitrate in kbps
        fps = video_info.get("fps", 0)
    
    quality_info = f"{width}x{height}" if width and height else "unknown"
    if bitrate:
        quality_info += f" @{bitrate:.0f}kbps"
    
    activity.logger.info(
        f"✅ Downloaded video {video_index}: "
        f"{os.path.basename(output_path)} ({file_size / 1024 / 1024:.2f} MB, "
        f"{duration:.1f}s, {quality_info}, phash: {perceptual_hash.split(':')[1][:8]}...)"
    )
    
    return {
        "status": "success",
        "file_path": output_path,
        "file_hash": file_hash,
        "perceptual_hash": perceptual_hash,
        "duration": duration,
        "file_size": file_size,
        "video_index": video_index,
        "source_url": source_tweet_url,
        # Quality metadata for better deduplication
        "width": width,
        "height": height,
        "bitrate": bitrate,
        "fps": fps,
        "resolution_score": width * height,  # Higher = better quality
    }


@activity.defn
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_hashes: Optional[List[str]] = None
) -> List[Dict[str, Any]]:
    """
    Deduplicate videos by hash WITHIN this download batch and AGAINST existing S3 videos.
    
    Two levels of deduplication:
    1. Cross-retry: Filter out videos we already have in S3 (from previous retries)
    2. Within-batch: Find duplicate videos in current download batch (same video, different tweets)
    
    Tracks duplicate count as 'quality score' - videos found multiple times
    are likely better quality/more relevant.
    
    Args:
        downloaded_files: List of download results with file_path, file_hash, file_size
        existing_s3_hashes: List of MD5 hashes already in S3 (from previous retries)
    
    Returns:
        List of unique videos with duplicate_count field (duplicates removed from disk)
    """
    # Filter out failed and filtered downloads
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    filtered_count = sum(1 for f in downloaded_files if f.get("status") == "filtered")
    
    if filtered_count > 0:
        activity.logger.info(f"🚫 Filtered out {filtered_count} videos by duration")
    
    if not successful:
        activity.logger.warning("⚠️ No successful downloads to deduplicate")
        return []
    
    # Convert existing S3 perceptual hashes to set for fast lookup
    existing_hashes = set(existing_s3_hashes) if existing_s3_hashes else set()
    if existing_hashes:
        activity.logger.info(f"📦 Checking against {len(existing_hashes)} existing S3 video hashes...")
    
    seen_hashes = {}  # perceptual_hash -> {file_info, duplicate_count}
    duplicates_removed = 0
    already_in_s3_count = 0
    
    for file_info in successful:
        perceptual_hash = file_info["perceptual_hash"]
        file_path = file_info["file_path"]
        file_size = file_info["file_size"]
        duration = file_info["duration"]
        
        # Check if this video already exists in S3 (from previous retry)
        # Compare perceptual hashes
        already_exists = False
        for existing_hash in existing_hashes:
            if _perceptual_hashes_match(perceptual_hash, existing_hash):
                already_exists = True
                break
        
        if already_exists:
            activity.logger.info(
                f"♻️ Video already in S3 ({duration:.1f}s, perceptual match), removing local copy"
            )
            os.remove(file_path)
            already_in_s3_count += 1
            continue
        
        # Check for duplicates in current batch using perceptual hash
        found_duplicate = False
        for seen_hash, existing in seen_hashes.items():
            if _perceptual_hashes_match(perceptual_hash, seen_hash):
                found_duplicate = True
                # Duplicate found - increment quality score!
                existing["duplicate_count"] += 1
                
                # Determine which to keep based on quality (resolution > bitrate > size)
                new_resolution = file_info.get("resolution_score", 0)
                existing_resolution = existing.get("resolution_score", 0)
                
                new_bitrate = file_info.get("bitrate", 0)
                existing_bitrate = existing.get("bitrate", 0)
                
                # Decision logic: resolution first, then bitrate, then file size
                keep_new = False
                reason = ""
                
                if new_resolution > existing_resolution:
                    keep_new = True
                    reason = f"higher resolution ({file_info.get('width')}x{file_info.get('height')} > {existing.get('width')}x{existing.get('height')})"
                elif new_resolution == existing_resolution and new_bitrate > existing_bitrate:
                    keep_new = True
                    reason = f"higher bitrate ({new_bitrate:.0f}kbps > {existing_bitrate:.0f}kbps)"
                elif new_resolution == existing_resolution and new_bitrate == existing_bitrate and file_size > existing["file_size"]:
                    keep_new = True
                    reason = f"larger file ({file_size} > {existing['file_size']})"
                else:
                    reason = "existing is higher quality"
                
                if keep_new:
                    activity.logger.info(
                        f"🔄 Duplicate #{existing['duplicate_count']}: keeping new ({reason}, {duration:.1f}s)"
                    )
                    os.remove(existing["file_path"])
                    file_info["duplicate_count"] = existing["duplicate_count"]
                    # Replace old hash with new one
                    del seen_hashes[seen_hash]
                    seen_hashes[perceptual_hash] = file_info
                else:
                    activity.logger.info(
                        f"🔄 Duplicate #{existing['duplicate_count']}: keeping existing ({reason}, {duration:.1f}s)"
                    )
                    os.remove(file_path)
                
                duplicates_removed += 1
                break  # Found duplicate, stop checking
        
        if not found_duplicate:
            # First time seeing this video
            file_info["duplicate_count"] = 1
            seen_hashes[perceptual_hash] = file_info
    
    unique_videos = list(seen_hashes.values())
    
    # Sort by quality score (duplicate_count) descending - best videos first
    unique_videos.sort(key=lambda v: v.get("duplicate_count", 1), reverse=True)
    
    # Log results
    if already_in_s3_count > 0:
        activity.logger.info(
            f"♻️ Filtered out {already_in_s3_count} videos already in S3 (from previous retries)"
        )
    
    if duplicates_removed > 0:
        top_video = unique_videos[0]
        activity.logger.info(
            f"✅ Deduplication complete: {len(unique_videos)} unique videos "
            f"({duplicates_removed} within-batch duplicates, {already_in_s3_count} already in S3). "
            f"Top video seen {top_video['duplicate_count']}x (hash: {top_video['file_hash'][:8]}...)"
        )
    elif already_in_s3_count == 0:
        activity.logger.info(f"✅ All {len(unique_videos)} videos unique (no duplicates)")
    else:
        activity.logger.info(
            f"✅ {len(unique_videos)} new unique videos ({already_in_s3_count} already in S3)"
        )
    
    return unique_videos


@activity.defn
async def validate_video_is_soccer(file_path: str, event_id: str) -> Dict[str, Any]:
    """
    AI validation to check if video is actually a soccer/football video.
    
    This prevents uploading non-soccer content (ads, interviews, random videos, etc.)
    to S3. Uses computer vision to detect soccer-specific features.
    
    TODO: Implement AI model inference:
    - Check for soccer field (green pitch, white lines)
    - Check for players in soccer uniforms
    - Check for ball movement
    - Check for goal posts/nets
    - Reject if: ads, still images, interviews, non-sports content
    
    Args:
        file_path: Local path to video file
        event_id: Event ID for logging
    
    Returns:
        Dict with is_valid (bool), confidence (float), reason (str)
    
    Raises:
        FileNotFoundError: If video file doesn't exist
    """
    # Verify file exists before validation
    if not os.path.exists(file_path):
        msg = f"❌ FATAL: Video file not found for validation: {file_path}"
        activity.logger.error(msg)
        raise FileNotFoundError(msg)
    
    # STUB: For now, assume all videos are valid
    # Future: Load AI model and run inference on video frames
    
    activity.logger.info(f"🤖 AI validation: {event_id} at {file_path} (STUB - assuming valid)")
    
    # TODO: Implement actual AI validation
    # import cv2
    # import torch
    # model = load_soccer_detection_model()
    # frames = extract_frames(file_path, num_frames=10)
    # predictions = model.predict(frames)
    # is_soccer = predictions['is_soccer'] > 0.8
    # confidence = predictions['confidence']
    
    return {
        "is_valid": True,  # Stub: assume valid
        "confidence": 1.0,
        "reason": "AI validation not yet implemented (stub)",
        "detected_features": [],  # Future: ["soccer_field", "players", "ball", "goal"]
    }


@activity.defn
async def upload_single_video(
    file_path: str,
    fixture_id: int,
    event_id: str,
    player_name: str,
    team_name: str,
    video_index: int,
    file_hash: str = "",
    perceptual_hash: str = "",
    duration: float = 0.0,
    quality_score: int = 1,
    assister_name: str = "",
    opponent_team: str = "",
    source_url: str = "",
) -> Dict[str, Any]:
    """
    Upload a single video to S3 with metadata and tags.
    
    Args:
        file_path: Local path to video file
        fixture_id: Fixture ID
        event_id: Event ID
        player_name: Player name for metadata
        team_name: Team name for metadata
        video_index: Index for logging
        file_hash: MD5 hash for S3 key (enables dedup checking)
        perceptual_hash: Perceptual hash for cross-resolution dedup
        duration: Video duration in seconds
        quality_score: Duplicate count (higher = better/more popular video)
    
    Returns:
        Dict with s3_url, perceptual_hash, and status
    
    Raises:
        Exception: If S3 upload fails (for Temporal retry)
    """
    from src.data.s3_store import FootyS3Store
    
    # Use hash in filename for uniqueness (no index needed)
    if file_hash:
        filename = f"{event_id}_{file_hash[:8]}.mp4"
    else:
        # Fallback without hash - use timestamp for uniqueness
        import time
        filename = f"{event_id}_{int(time.time())}.mp4"
    
    # S3 key: {fixture_id}/{event_id}/{filename}
    s3_key = f"{fixture_id}/{event_id}/{filename}"
    
    # Get display titles from MongoDB event
    from src.data.mongo_store import FootyMongoStore
    store = FootyMongoStore()
    
    display_title = ""
    display_subtitle = ""
    fixture_data = store.get_fixture_from_active(fixture_id)
    if fixture_data:
        for evt in fixture_data.get("events", []):
            if evt.get("_event_id") == event_id:
                display_title = evt.get("_display_title", "")
                display_subtitle = evt.get("_display_subtitle", "")
                break
    
    # Metadata (stored in object headers)
    metadata = {
        "player_name": player_name,
        "team_name": team_name,
        "event_id": event_id,
        "fixture_id": str(fixture_id),
        "quality_score": str(quality_score),
        "source_url": source_url,  # Original tweet URL for dedup tracking
        "display_title": display_title,
        "display_subtitle": display_subtitle,
        "perceptual_hash": perceptual_hash,  # For cross-resolution dedup
        "duration": str(duration),
    }
    
    if assister_name:
        metadata["assister_name"] = assister_name
    if opponent_team:
        metadata["opponent_team"] = opponent_team
    
    # Tags (better for filtering/search than metadata)
    # MinIO/S3 supports up to 10 tags per object
    tags = {
        "fixture_id": str(fixture_id),
        "event_id": event_id,
        "scorer": player_name.replace(" ", "_")[:50],  # Player who scored
        "team": team_name.replace(" ", "_")[:50],  # Team who scored
        "quality_score": str(quality_score),
        "video_index": str(video_index),
    }
    
    # Add assister if available (from event data)
    if assister_name:
        tags["assister"] = assister_name.replace(" ", "_")[:50]
    
    # Add opponent team (from event data - team scored against)
    if opponent_team:
        tags["opponent"] = opponent_team.replace(" ", "_")[:50]
    
    activity.logger.info(
        f"☁️ Uploading video {video_index} to S3: {s3_key} "
        f"(quality_score={quality_score})"
    )
    
    s3_store = FootyS3Store()
    s3_url = s3_store.upload_video(file_path, s3_key, metadata=metadata, tags=tags)
    
    if not s3_url:
        msg = f"❌ Upload failed for video {video_index}: S3 store returned None"
        activity.logger.error(msg)
        raise RuntimeError(msg)
    
    activity.logger.info(f"✅ Uploaded video {video_index}: {s3_url}")
    return {
        "status": "success",
        "s3_url": s3_url,
        "perceptual_hash": perceptual_hash,
        "quality_score": quality_score
    }


@activity.defn
async def mark_download_complete(
    fixture_id: int,
    event_id: str,
    s3_urls: List[str],
    perceptual_hashes: List[str],
    temp_dir: str,
) -> bool:
    """
    Save download results (S3 URLs and perceptual hashes) and cleanup temp directory.
    
    Note: Monitor workflow controls _twitter_count and _twitter_complete flags.
    This function just saves the results of the download attempt.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        s3_urls: List of S3 URLs that were uploaded
        perceptual_hashes: List of perceptual hashes for cross-resolution dedup
        temp_dir: Temporary directory to cleanup
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    import shutil
    
    store = FootyMongoStore()
    
    # Save results to MongoDB
    activity.logger.info(f"💾 Saving {len(s3_urls)} videos for {event_id}")
    success = store.mark_event_download_complete(
        fixture_id, event_id, s3_urls, perceptual_hashes
    )
    
    if not success:
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


def _get_video_duration(file_path: str) -> float:
    """
    Get video duration in seconds using ffprobe.
    
    Args:
        file_path: Path to video file
        
    Returns:
        Duration in seconds, or 0.0 if failed
    """
    import subprocess
    import json
    
    try:
        cmd = [
            "ffprobe",
            "-v", "quiet",
            "-print_format", "json",
            "-show_format",
            file_path
        ]
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
        if result.returncode == 0:
            data = json.loads(result.stdout)
            duration = float(data.get("format", {}).get("duration", 0))
            return duration
    except Exception as e:
        activity.logger.warning(f"⚠️ Failed to get duration for {file_path}: {e}")
    
    return 0.0


def _generate_perceptual_hash(file_path: str, file_size: int) -> str:
    """
    Generate perceptual hash from file size and 3 video frames at 1s, 2s, and 3s.
    
    Uses difference hash (dHash) algorithm for each frame:
    1. Extract frame at timestamp
    2. Resize to 9x8 (for 8x8 hash)
    3. Convert to grayscale
    4. Compare adjacent pixels to create 64-bit hash
    
    Args:
        file_path: Path to video file
        file_size: File size in bytes (more precise than duration)
        
    Returns:
        Composite hash string: "filesize:hash1s:hash2s:hash3s"
        Example: "4412345:a3f8b2e1c9d4:f5a21e3b8c7d:9f2a4b1c3e5d"
    """
    import subprocess
    from PIL import Image
    import io
    
    def extract_frame_hash(timestamp: float) -> str:
        """Extract frame at timestamp and compute dHash"""
        try:
            # Use ffmpeg to extract single frame
            cmd = [
                "ffmpeg",
                "-ss", str(timestamp),
                "-i", file_path,
                "-vframes", "1",
                "-f", "image2pipe",
                "-vcodec", "png",
                "-"
            ]
            result = subprocess.run(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                timeout=10
            )
            
            if result.returncode != 0:
                return "0" * 16  # Return zeros if frame extraction fails
            
            # Load image and compute dHash
            img = Image.open(io.BytesIO(result.stdout))
            img = img.convert('L')  # Grayscale
            img = img.resize((9, 8), Image.Resampling.LANCZOS)  # 9x8 for 8x8 hash
            
            pixels = list(img.getdata())
            
            # Compute difference hash (compare adjacent horizontal pixels)
            hash_bits = []
            for row in range(8):
                for col in range(8):
                    left = pixels[row * 9 + col]
                    right = pixels[row * 9 + col + 1]
                    hash_bits.append('1' if left < right else '0')
            
            # Convert binary to hex
            hash_int = int(''.join(hash_bits), 2)
            return format(hash_int, '016x')  # 16 hex chars (64 bits)
            
        except Exception as e:
            activity.logger.warning(f"⚠️ Failed to extract frame at {timestamp}s: {e}")
            return "0" * 16
    
    # Extract hashes at 1s, 2s, 3s
    hash_1s = extract_frame_hash(1.0)
    hash_2s = extract_frame_hash(2.0)
    hash_3s = extract_frame_hash(3.0)
    
    # Combine into single string (file_size is exact, better than duration)
    composite = f"{file_size}:{hash_1s}:{hash_2s}:{hash_3s}"
    return composite


def _perceptual_hashes_match(hash_a: str, hash_b: str) -> bool:
    """
    Check if two perceptual hashes represent the same video.
    
    Match criteria:
    - File size within ±1% (allows for minor re-encoding differences)
    - All 3 frame hashes must match exactly (3/3)
    
    Args:
        hash_a: First hash (format: "filesize:hash1:hash2:hash3")
        hash_b: Second hash (format: "filesize:hash1:hash2:hash3")
        
    Returns:
        True if videos match
    """
    try:
        parts_a = hash_a.split(':')
        parts_b = hash_b.split(':')
        
        if len(parts_a) != 4 or len(parts_b) != 4:
            return False
        
        size_a = int(parts_a[0])
        size_b = int(parts_b[0])
        
        # File size check (within ±1% to allow for minor re-encoding)
        size_diff_pct = abs(size_a - size_b) / max(size_a, size_b)
        if size_diff_pct > 0.01:  # More than 1% difference
            return False
        
        # All 3 frame hashes must match (strict)
        return (
            parts_a[1] == parts_b[1] and
            parts_a[2] == parts_b[2] and
            parts_a[3] == parts_b[3]
        )
    except Exception:
        return False


def _convert_cookies_to_netscape(json_file: str, netscape_file: str) -> None:
    """
    Convert Selenium JSON cookies to Netscape format for yt-dlp.
    
    Netscape format (tab-separated):
    domain\tflag\tpath\tsecure\texpiration\tname\tvalue
    
    Args:
        json_file: Path to JSON cookie file from Selenium
        netscape_file: Path to write Netscape format cookies
    """
    import json
    
    with open(json_file, 'r') as f:
        data = json.load(f)
    
    cookies = data.get('cookies', [])
    
    with open(netscape_file, 'w') as f:
        # Netscape cookie file header
        f.write("# Netscape HTTP Cookie File\n")
        f.write("# This is a generated file! Do not edit.\n\n")
        
        for cookie in cookies:
            domain = cookie.get('domain', '.x.com')
            flag = 'TRUE' if domain.startswith('.') else 'FALSE'
            path = cookie.get('path', '/')
            secure = 'TRUE' if cookie.get('secure', True) else 'FALSE'
            expiration = str(int(cookie.get('expiry', 0)))
            name = cookie.get('name', '')
            value = cookie.get('value', '')
            
            # Write tab-separated line
            f.write(f"{domain}\t{flag}\t{path}\t{secure}\t{expiration}\t{name}\t{value}\n")

