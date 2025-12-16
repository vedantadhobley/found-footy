"""Download activities - Granular per-video operations with individual retry"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import tempfile
import hashlib
import yt_dlp
import asyncio
import random
import time

# Global lock and timestamp to rate-limit downloads across all workers
_download_lock = asyncio.Lock()
_last_download_time = 0


@activity.defn
async def fetch_event_data(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Fetch event from fixtures_active and return discovered videos.
    Also checks S3 for existing videos with full metadata for quality comparison.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
    
    Returns:
        Dict with discovered_videos, player_name, team_name, event, existing_s3_videos
        existing_s3_videos contains full metadata including quality info for replacement decisions
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
    
    # Get existing videos from MongoDB (new _s3_videos schema)
    existing_s3_videos_mongo = event.get("_s3_videos", [])
    
    # Build full metadata for existing S3 videos (for quality comparison)
    # Enrich MongoDB data with S3 metadata for quality comparison
    existing_s3_videos = []  # List of {s3_url, s3_key, perceptual_hash, width, height, bitrate, file_size, source_url, popularity}
    already_downloaded_urls = set()
    
    if existing_s3_videos_mongo:
        s3_store = FootyS3Store()
        
        for video_obj in existing_s3_videos_mongo:
            s3_url = video_obj.get("url", "")
            if not s3_url:
                continue
                
            try:
                # Extract S3 key from relative path
                # Format: /video/footy-videos/{fixture}/{event_id}/file.mp4
                if not s3_url.startswith("/video/footy-videos/"):
                    activity.logger.warning(f"⚠️ Unexpected S3 URL format: {s3_url}")
                    continue
                
                s3_key = s3_url.replace("/video/footy-videos/", "")
                
                # Use boto3's head_object to get full metadata from S3
                response = s3_store.s3_client.head_object(Bucket="footy-videos", Key=s3_key)
                metadata = response.get("Metadata", {})
                
                source_url = metadata.get("source_url", "")
                if source_url:
                    already_downloaded_urls.add(source_url)
                
                # Build full video info for quality comparison (S3 metadata + MongoDB data)
                video_info = {
                    "s3_url": s3_url,
                    "s3_key": s3_key,
                    "perceptual_hash": video_obj.get("perceptual_hash", "") or metadata.get("perceptual_hash", ""),
                    "width": int(metadata.get("width", 0)),
                    "height": int(metadata.get("height", 0)),
                    "bitrate": float(metadata.get("bitrate", 0)),
                    "file_size": int(metadata.get("file_size", 0)),
                    "source_url": source_url,
                    "duration": float(metadata.get("duration", 0)),
                    "resolution_score": video_obj.get("resolution_score", 0) or int(metadata.get("resolution_score", 0)),
                    "popularity": video_obj.get("popularity", 1),  # From MongoDB
                }
                existing_s3_videos.append(video_info)
                activity.logger.debug(f"✓ Existing S3 video: {s3_key} ({video_info['width']}x{video_info['height']}, pop={video_info['popularity']})")
            except Exception as e:
                # If we can't get metadata, still track the URL but with limited info
                activity.logger.warning(f"⚠️ Failed to get S3 metadata for {s3_url}: {e}")
                existing_s3_videos.append({
                    "s3_url": s3_url,
                    "perceptual_hash": video_obj.get("perceptual_hash", ""),
                    "resolution_score": video_obj.get("resolution_score", 0),
                    "popularity": video_obj.get("popularity", 1),
                })
    
    # Filter discovered_videos to only NEW ones (URLs not already downloaded)
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
            "existing_s3_videos": existing_s3_videos,
        }
    
    if existing_s3_videos:
        activity.logger.info(f"🔍 Have {len(existing_s3_videos)} existing S3 videos for cross-retry quality comparison")
    
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
        "existing_s3_videos": existing_s3_videos,  # Full metadata for quality comparison
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
    import subprocess
    import time
    import json
    
    # Ensure temp directory exists (activity can do I/O)
    os.makedirs(temp_dir, exist_ok=True)
    
    output_path = os.path.join(temp_dir, f"{event_id}_{video_index}.mp4")
    
    activity.logger.info(f"📥 Downloading video {video_index}: {video_url[:50]}...")
    
    # Use shared cookie file from /config mount
    cookies_json_file = "/config/twitter_cookies.json"
    
    if not os.path.exists(cookies_json_file):
        msg = f"❌ No cookies found at {cookies_json_file} - cannot download"
        activity.logger.error(msg)
        raise RuntimeError(msg)
    
    # Convert cookies to Netscape format for yt-dlp
    temp_cookie_netscape = f"/tmp/cookies_{int(time.time())}_{video_index}.txt"
    try:
        with open(cookies_json_file, 'r') as f:
            data = json.load(f)
        
        cookies = data.get('cookies', [])
        with open(temp_cookie_netscape, 'w') as f:
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
                f.write(f"{domain}\t{flag}\t{path}\t{secure}\t{expiration}\t{name}\t{value}\n")
        
        # Run yt-dlp directly in worker with shared cookies - fast timeout
        result = subprocess.run([
            'yt-dlp',
            '--cookies', temp_cookie_netscape,
            '--format', 'best[ext=mp4]/best',
            '--output', output_path,
            '--no-warnings',
            '--quiet',
            '--socket-timeout', '15',
            '--retries', '1',
            video_url
        ], capture_output=True, text=True, timeout=30)
        
        # Cleanup temp cookie file
        try:
            os.remove(temp_cookie_netscape)
        except:
            pass
        
        if result.returncode != 0:
            error_msg = result.stderr[:200] if result.stderr else "Unknown error"
            activity.logger.warning(f"⚠️ yt-dlp failed: {error_msg}")
            raise RuntimeError(f"yt-dlp failed: {error_msg}")
            
    except subprocess.TimeoutExpired:
        activity.logger.warning(f"⚠️ Download timed out for video {video_index}")
        raise RuntimeError("Download timed out")
    except Exception as e:
        activity.logger.warning(f"⚠️ Download failed: {str(e)[:100]}")
        raise
    
    video_info = None  # No metadata from yt-dlp in quiet mode
    
    if not os.path.exists(output_path):
        msg = f"❌ Download failed for video {video_index}: file does not exist after yt-dlp"
        activity.logger.error(msg)
        raise RuntimeError(msg)
    
    # Get video metadata (duration, resolution, bitrate)
    video_meta = _get_video_metadata(output_path)
    duration = video_meta["duration"]
    width = video_meta["width"]
    height = video_meta["height"]
    bitrate = video_meta["bitrate"]
    
    # Duration filter: 5-60 seconds (typical goal clips)
    if duration < 5.0:
        activity.logger.warning(
            f"⏱️ Video {video_index} too short ({duration:.1f}s < 5s), skipping"
        )
        os.remove(output_path)
        return {"status": "filtered", "reason": "too_short", "duration": duration, "source_url": source_tweet_url}
    
    if duration > 60.0:
        activity.logger.warning(
            f"⏱️ Video {video_index} too long ({duration:.1f}s > 60s), skipping"
        )
        os.remove(output_path)
        return {"status": "filtered", "reason": "too_long", "duration": duration, "source_url": source_tweet_url}
    
    # Aspect ratio filter: reject non-landscape videos
    # Goal clips should be landscape format (at least 4:3 aspect ratio = 1.33)
    # This filters out:
    #   - Vertical videos (9:16 phone recordings, TikTok reposts)
    #   - Square videos (1:1 club promo clips, social media posts)
    MIN_ASPECT_RATIO = 1.33  # 4:3
    if width and height:
        aspect_ratio = width / height
        if aspect_ratio < MIN_ASPECT_RATIO:
            activity.logger.warning(
                f"📐 Video {video_index} aspect ratio too narrow ({width}x{height} = {aspect_ratio:.2f} < {MIN_ASPECT_RATIO}), skipping"
            )
            os.remove(output_path)
            return {"status": "filtered", "reason": "aspect_ratio", "width": width, "height": height, "aspect_ratio": aspect_ratio, "source_url": source_tweet_url}
    
    # Calculate MD5 hash and size first
    file_hash = _calculate_md5(output_path)
    file_size = os.path.getsize(output_path)
    
    # Generate perceptual hash (duration + frames at 1s, 2s, 3s)
    # Duration is content-invariant - same video from different sources has same duration
    perceptual_hash = _generate_perceptual_hash(output_path, duration)
    
    quality_info = f"{width}x{height}" if width and height else "unknown res"
    if bitrate:
        quality_info += f"@{bitrate:.0f}kbps"
    
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
        # Quality metadata for cross-retry deduplication
        "width": width,
        "height": height,
        "bitrate": bitrate,
        "resolution_score": width * height,  # Higher = better quality
    }


@activity.defn
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None
) -> Dict[str, Any]:
    """
    Deduplicate videos by perceptual hash WITHIN batch and AGAINST existing S3 videos.
    
    Three outcomes for each downloaded video:
    1. NEW: No match in S3 or batch -> upload it
    2. REPLACE: Matches S3 video but new is higher quality -> upload and delete old
    3. SKIP: Matches S3 video but existing is higher quality -> skip (track URL for dedup)
    4. BATCH_DUP: Matches another video in this batch -> keep higher quality
    
    Quality comparison: resolution > bitrate > file_size
    
    Args:
        downloaded_files: List of download results with file_path, file_hash, file_size, quality metadata
        existing_s3_videos: List of existing S3 video metadata from fetch_event_data
    
    Returns:
        Dict with:
        - videos_to_upload: List of unique videos to upload
        - videos_to_replace: List of {new_video, old_s3_video} for replacement
        - skipped_urls: List of source URLs that matched lower-quality existing videos
    """
    # Filter out failed and filtered downloads
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    filtered_count = sum(1 for f in downloaded_files if f.get("status") == "filtered")
    
    if filtered_count > 0:
        activity.logger.info(f"🚫 Filtered out {filtered_count} videos by duration")
    
    if not successful:
        activity.logger.warning("⚠️ No successful downloads to deduplicate")
        return {"videos_to_upload": [], "videos_to_replace": [], "videos_to_bump_popularity": [], "skipped_urls": []}
    
    # Build lookup for existing S3 videos by perceptual hash
    existing_videos_list = existing_s3_videos or []
    if existing_videos_list:
        activity.logger.info(f"📦 Checking against {len(existing_videos_list)} existing S3 videos...")
    
    # Track results
    videos_to_upload = []  # New videos (no match in S3)
    videos_to_replace = []  # Higher quality than existing S3 video
    videos_to_bump_popularity = []  # Existing is better but still track the duplicate
    skipped_urls = []  # URLs of videos we're not uploading (existing is better)
    
    seen_hashes = {}  # perceptual_hash -> {file_info, duplicate_count}
    duplicates_removed = 0
    
    for file_info in successful:
        perceptual_hash = file_info["perceptual_hash"]
        file_path = file_info["file_path"]
        file_size = file_info["file_size"]
        duration = file_info["duration"]
        source_url = file_info.get("source_url", "")
        
        # === CROSS-RETRY DEDUP: Check against existing S3 videos ===
        matched_existing = None
        for existing in existing_videos_list:
            existing_hash = existing.get("perceptual_hash", "")
            if existing_hash and _perceptual_hashes_match(perceptual_hash, existing_hash):
                matched_existing = existing
                break
        
        if matched_existing:
            # Found match in S3 - compare quality by file size (larger = better quality/bitrate)
            new_file_size = file_size
            existing_file_size = matched_existing.get("file_size", 0)
            
            # Quality comparison: file_size only - larger files have better quality/bitrate
            keep_new = False
            reason = ""
            
            if new_file_size > existing_file_size:
                keep_new = True
                reason = f"larger file ({new_file_size:,} > {existing_file_size:,} bytes)"
            else:
                reason = f"existing is same or larger ({existing_file_size:,} bytes)"
            
            if keep_new:
                # New video is better - mark for replacement
                # IMPORTANT: Inherit popularity from the video we're replacing + 1
                # This preserves the dedup history (e.g., if old had popularity=3, new gets 4)
                existing_popularity = matched_existing.get("popularity", 1)
                activity.logger.info(
                    f"🔄 REPLACE: New video is {reason} ({duration:.1f}s) - inheriting popularity {existing_popularity} + 1"
                )
                file_info["duplicate_count"] = existing_popularity + 1
                videos_to_replace.append({
                    "new_video": file_info,
                    "old_s3_video": matched_existing
                })
            else:
                # Existing is better - skip but track URL
                # Still increment popularity since this is another "vote" for this content
                existing_popularity = matched_existing.get("popularity", 1)
                activity.logger.info(
                    f"⏭️ SKIP: Existing S3 video is {reason} ({duration:.1f}s), bumping popularity {existing_popularity} → {existing_popularity + 1}"
                )
                videos_to_bump_popularity.append({
                    "s3_video": matched_existing,
                    "new_popularity": existing_popularity + 1
                })
                os.remove(file_path)
                if source_url:
                    skipped_urls.append(source_url)
            continue
        
        # === WITHIN-BATCH DEDUP: Check against other downloads in this batch ===
        found_batch_dup = False
        for seen_hash, existing in list(seen_hashes.items()):
            if _perceptual_hashes_match(perceptual_hash, seen_hash):
                found_batch_dup = True
                existing["duplicate_count"] += 1
                
                # Compare by file_size only - larger file = better quality
                new_file_size = file_size
                existing_file_size = existing.get("file_size", 0)
                
                keep_new = False
                reason = ""
                
                if new_file_size > existing_file_size:
                    keep_new = True
                    reason = f"larger file ({new_file_size} > {existing_file_size})"
                else:
                    reason = f"existing is same or larger file ({existing_file_size} >= {new_file_size})"
                
                if keep_new:
                    activity.logger.info(
                        f"🔄 Batch dup #{existing['duplicate_count']}: keeping new ({reason})"
                    )
                    os.remove(existing["file_path"])
                    file_info["duplicate_count"] = existing["duplicate_count"]
                    del seen_hashes[seen_hash]
                    seen_hashes[perceptual_hash] = file_info
                else:
                    activity.logger.info(
                        f"🔄 Batch dup #{existing['duplicate_count']}: keeping existing ({reason})"
                    )
                    os.remove(file_path)
                
                duplicates_removed += 1
                break
        
        if not found_batch_dup:
            # New unique video
            file_info["duplicate_count"] = 1
            seen_hashes[perceptual_hash] = file_info
    
    # Collect videos to upload (from batch dedup)
    videos_to_upload = list(seen_hashes.values())
    
    # Sort by quality score
    videos_to_upload.sort(key=lambda v: v.get("duplicate_count", 1), reverse=True)
    
    # Log summary
    activity.logger.info(
        f"✅ Deduplication complete: "
        f"{len(videos_to_upload)} new, "
        f"{len(videos_to_replace)} replacements, "
        f"{len(skipped_urls)} skipped (existing better), "
        f"{len(videos_to_bump_popularity)} popularity bumps, "
        f"{duplicates_removed} batch dups"
    )
    
    return {
        "videos_to_upload": videos_to_upload,
        "videos_to_replace": videos_to_replace,
        "videos_to_bump_popularity": videos_to_bump_popularity,
        "skipped_urls": skipped_urls
    }


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
    popularity: int = 1,
    assister_name: str = "",
    opponent_team: str = "",
    source_url: str = "",
    width: int = 0,
    height: int = 0,
    bitrate: float = 0.0,
    file_size: int = 0,
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
        popularity: Duplicate count (higher = more sources found this video = more trustworthy)
    
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
    
    # Metadata (stored in object headers) - includes quality info for cross-retry dedup
    metadata = {
        "player_name": player_name,
        "team_name": team_name,
        "event_id": event_id,
        "fixture_id": str(fixture_id),
        "popularity": str(popularity),  # How many times this video was seen (dedup count)
        "source_url": source_url,  # Original tweet URL for dedup tracking
        "display_title": display_title,
        "display_subtitle": display_subtitle,
        "perceptual_hash": perceptual_hash,  # For cross-resolution dedup
        "duration": str(duration),
        # Quality metrics for cross-retry quality comparison
        "width": str(width),
        "height": str(height),
        "bitrate": str(int(bitrate)) if bitrate else "0",
        "file_size": str(file_size),
        "resolution_score": str(width * height) if width and height else "0",
    }
    
    if assister_name:
        metadata["assister_name"] = assister_name
    if opponent_team:
        metadata["opponent_team"] = opponent_team
    
    quality_info = f"{width}x{height}" if width and height else "unknown"
    if bitrate:
        quality_info += f"@{int(bitrate)}kbps"
    activity.logger.info(
        f"☁️ Uploading video {video_index} to S3: {s3_key} "
        f"({quality_info}, popularity={popularity})"
    )
    
    s3_store = FootyS3Store()
    s3_url = s3_store.upload_video(file_path, s3_key, metadata=metadata)
    
    if not s3_url:
        msg = f"❌ Upload failed for video {video_index}: S3 store returned None"
        activity.logger.error(msg)
        raise RuntimeError(msg)
    
    activity.logger.info(f"✅ Uploaded video {video_index}: {s3_url}")
    
    # Return video object for MongoDB storage
    resolution_score = width * height if width and height else 0
    return {
        "status": "success",
        "s3_url": s3_url,
        "perceptual_hash": perceptual_hash,
        "resolution_score": resolution_score,
        "popularity": popularity,
        # Full video object for MongoDB _s3_videos array
        "video_object": {
            "url": s3_url,
            "perceptual_hash": perceptual_hash,
            "resolution_score": resolution_score,
            "popularity": popularity,
            "rank": 0,  # Will be recalculated
        }
    }


@activity.defn
async def mark_download_complete(
    fixture_id: int,
    event_id: str,
    video_objects: List[Dict[str, Any]],
    temp_dir: str,
) -> bool:
    """
    Save download results (video objects) and cleanup temp directory.
    Then recalculate ranks for all videos in this event.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        video_objects: List of video objects {url, perceptual_hash, resolution_score, popularity, rank}
        temp_dir: Temporary directory to cleanup
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    import shutil
    
    store = FootyMongoStore()
    
    # Save video objects to _s3_videos array
    activity.logger.info(f"💾 Saving {len(video_objects)} videos for {event_id}")
    success = store.add_videos_to_event(fixture_id, event_id, video_objects)
    
    if not success:
        activity.logger.warning(f"⚠️ Failed to update event {event_id}")
    else:
        # Recalculate ranks after adding new videos
        activity.logger.info(f"📊 Recalculating video ranks for {event_id}")
        store.recalculate_video_ranks(fixture_id, event_id)
    
    # Cleanup temp directory
    if temp_dir and os.path.exists(temp_dir):
        shutil.rmtree(temp_dir)
        activity.logger.info(f"🧹 Cleaned up temp directory: {temp_dir}")
    
    return success


@activity.defn
async def replace_s3_video(
    fixture_id: int,
    event_id: str,
    old_s3_url: str,
    old_s3_key: str,
) -> bool:
    """
    Delete an old S3 video and update MongoDB to remove it from _s3_videos.
    Called when a higher quality version is being uploaded to replace it.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        old_s3_url: Full S3 URL to remove from MongoDB
        old_s3_key: S3 key for deletion
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.s3_store import FootyS3Store
    
    # Delete from S3
    s3_store = FootyS3Store()
    try:
        s3_store.s3_client.delete_object(Bucket="footy-videos", Key=old_s3_key)
        activity.logger.info(f"🗑️ Deleted old S3 video: {old_s3_key}")
    except Exception as e:
        activity.logger.error(f"❌ Failed to delete old S3 video {old_s3_key}: {e}")
        # Continue - we still want to update MongoDB
    
    # Remove from MongoDB _s3_videos array (by URL)
    store = FootyMongoStore()
    try:
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, "events._event_id": event_id},
            {
                "$pull": {
                    "events.$._s3_videos": {"url": old_s3_url}
                }
            }
        )
        if result.modified_count > 0:
            activity.logger.info(f"✅ Removed old video from MongoDB: {old_s3_url}")
        else:
            activity.logger.warning(f"⚠️ Old video not found in MongoDB (may have been already removed)")
        return True
    except Exception as e:
        activity.logger.error(f"❌ Failed to update MongoDB after S3 delete: {e}")
        raise


@activity.defn
async def bump_video_popularity(
    fixture_id: int,
    event_id: str,
    s3_url: str,
    new_popularity: int,
) -> bool:
    """
    Bump the popularity count for an existing video.
    Called when we find a duplicate but the existing video is higher quality.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        s3_url: S3 URL of the existing video
        new_popularity: New popularity value
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    success = store.update_video_popularity(fixture_id, event_id, s3_url, new_popularity)
    
    if success:
        activity.logger.info(f"📈 Bumped popularity to {new_popularity} for existing video")
    else:
        activity.logger.warning(f"⚠️ Failed to bump popularity for {s3_url}")
    
    return success


@activity.defn
async def save_processed_urls(
    fixture_id: int,
    event_id: str,
    processed_urls: List[str],
) -> bool:
    """
    Save processed video URLs to MongoDB for deduplication tracking.
    These are URLs that we downloaded but didn't upload because:
    - We already have a higher quality version
    - The video matched an existing S3 video
    
    This ensures future Twitter searches don't re-discover these URLs.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        processed_urls: List of tweet URLs that were processed
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    
    if not processed_urls:
        return True
    
    store = FootyMongoStore()
    try:
        # Add URLs to _discovered_videos so they're skipped in future searches
        video_url_dicts = [{"video_page_url": url, "tweet_url": url} for url in processed_urls]
        
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, "events._event_id": event_id},
            {
                "$push": {
                    "events.$._discovered_videos": {"$each": video_url_dicts}
                }
            }
        )
        if result.modified_count > 0:
            activity.logger.info(f"✅ Saved {len(processed_urls)} processed URLs for dedup tracking")
        return True
    except Exception as e:
        activity.logger.error(f"❌ Failed to save processed URLs: {e}")
        return False


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


def _get_video_metadata(file_path: str) -> dict:
    """
    Get video metadata (width, height, bitrate) using ffprobe.
    
    Args:
        file_path: Path to video file
        
    Returns:
        Dict with width, height, bitrate, duration
    """
    import subprocess
    import json
    
    result = {
        "width": 0,
        "height": 0,
        "bitrate": 0.0,
        "duration": 0.0,
    }
    
    try:
        cmd = [
            "ffprobe",
            "-v", "quiet",
            "-print_format", "json",
            "-show_format",
            "-show_streams",
            file_path
        ]
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=10)
        if proc.returncode == 0:
            data = json.loads(proc.stdout)
            
            # Get duration from format
            format_info = data.get("format", {})
            result["duration"] = float(format_info.get("duration", 0))
            
            # Get bitrate from format (in bits/s, convert to kbps)
            bit_rate = format_info.get("bit_rate")
            if bit_rate:
                result["bitrate"] = float(bit_rate) / 1000  # Convert to kbps
            
            # Find video stream for resolution
            for stream in data.get("streams", []):
                if stream.get("codec_type") == "video":
                    result["width"] = stream.get("width", 0)
                    result["height"] = stream.get("height", 0)
                    break
                    
    except Exception as e:
        activity.logger.warning(f"⚠️ Failed to get video metadata for {file_path}: {e}")
    
    return result


def _generate_perceptual_hash(file_path: str, duration: float) -> str:
    """
    Generate perceptual hash from video duration and 3 video frames at 1s, 2s, and 3s.
    
    Uses difference hash (dHash) algorithm for each frame:
    1. Extract frame at timestamp
    2. Resize to 9x8 (for 8x8 hash)
    3. Convert to grayscale
    4. Compare adjacent pixels to create 64-bit hash
    
    Duration is a better identifier than file size because:
    - Same video from different sources has same duration
    - Different quality encodings have different file sizes but same duration
    - File size varies with compression, duration is content-invariant
    
    Args:
        file_path: Path to video file
        duration: Video duration in seconds
        
    Returns:
        Composite hash string: "duration:hash1s:hash2s:hash3s"
        Example: "12.5:a3f8b2e1c9d4:f5a21e3b8c7d:9f2a4b1c3e5d"
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
    
    # Combine into single string (duration is content-invariant, better for dedup)
    composite = f"{duration:.2f}:{hash_1s}:{hash_2s}:{hash_3s}"
    return composite


def _hamming_distance(hex_a: str, hex_b: str) -> int:
    """
    Calculate hamming distance (bit difference) between two hex hashes.
    
    Args:
        hex_a: First hex hash (e.g., "1fcee5dad69a67cc")
        hex_b: Second hex hash
        
    Returns:
        Number of differing bits (0-64 for 64-bit hashes)
    """
    try:
        int_a = int(hex_a, 16)
        int_b = int(hex_b, 16)
        return bin(int_a ^ int_b).count('1')
    except (ValueError, TypeError):
        return 64  # Max distance on error


def _perceptual_hashes_match(
    hash_a: str, 
    hash_b: str, 
    duration_tolerance: float = 0.5,
    max_hamming_per_frame: int = 8,
    max_total_hamming: int = 12
) -> bool:
    """
    Check if two perceptual hashes represent the same video.
    
    Match criteria:
    - Duration within ±0.5 seconds (same video, different encodings)
    - Frame hashes must be similar (using hamming distance)
      - Each frame hash can differ by at most 8 bits (out of 64)
      - Total difference across all 3 frames can be at most 12 bits (out of 192)
    
    The hamming distance tolerance accounts for:
    - Different video encodings (h264 vs h265, different bitrates)
    - Slight pixel differences from compression
    - Minor timestamp drift in frame extraction
    
    Args:
        hash_a: First hash (format: "duration:hash1:hash2:hash3")
        hash_b: Second hash (format: "duration:hash1:hash2:hash3")
        duration_tolerance: Max duration difference in seconds (default 0.5s)
        max_hamming_per_frame: Max bit difference per frame hash (default 8/64 = 12.5%)
        max_total_hamming: Max total bit difference across all frames (default 12/192 = 6.25%)
        
    Returns:
        True if videos match
    """
    try:
        parts_a = hash_a.split(':')
        parts_b = hash_b.split(':')
        
        if len(parts_a) != 4 or len(parts_b) != 4:
            return False
        
        duration_a = float(parts_a[0])
        duration_b = float(parts_b[0])
        
        # Duration check (within ±0.5s to allow for encoding differences)
        duration_diff = abs(duration_a - duration_b)
        if duration_diff > duration_tolerance:
            return False
        
        # Check hamming distance for each frame hash
        total_hamming = 0
        for i in range(1, 4):
            frame_hamming = _hamming_distance(parts_a[i], parts_b[i])
            if frame_hamming > max_hamming_per_frame:
                return False
            total_hamming += frame_hamming
        
        # Check total hamming distance across all frames
        return total_hamming <= max_total_hamming
        
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


def _try_direct_download(video_url: str, output_path: str, cookies_json: str, activity) -> bool:
    """
    Try to download video using the Twitter browser session via API call.
    
    This sends a request to the Twitter service (Selenium browser) to download
    the video, which bypasses yt-dlp's detectable patterns entirely.
    
    Returns:
        True if download succeeded, False otherwise
    """
    import requests
    import time
    
    try:
        activity.logger.info(f"🔄 Attempting browser-based download (bypass yt-dlp)")
        
        twitter_service_url = os.getenv('TWITTER_SERVICE_URL', 'http://found-footy-twitter:8888')
        
        # Request the Twitter service to download the video
        response = requests.post(
            f"{twitter_service_url}/download_video",
            json={
                "video_url": video_url,
                "output_path": output_path,
            },
            timeout=45,  # 45s total for download (matches activity timeout)
        )
        
        if response.status_code == 200:
            result = response.json()
            if result.get("status") == "success":
                file_size = os.path.getsize(output_path) if os.path.exists(output_path) else 0
                activity.logger.info(f"✅ Browser download succeeded ({file_size / 1024 / 1024:.2f} MB)")
                return True
            else:
                activity.logger.warning(f"⚠️ Browser download failed: {result.get('error', 'Unknown error')}")
                return False
        else:
            activity.logger.warning(f"⚠️ Browser download service returned {response.status_code}")
            return False
        
    except Exception as e:
        activity.logger.warning(f"⚠️ Browser download failed: {str(e)[:200]}")
        return False