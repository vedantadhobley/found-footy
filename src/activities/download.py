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

from src.data.models import EventFields
from src.utils.config import (
    LLAMA_CHAT_URL,
    ASPECT_RATIO_FILTER_ENABLED,
    MIN_ASPECT_RATIO,
    MIN_VIDEO_DURATION,
    MAX_VIDEO_DURATION,
    HASH_VERSION,
    MAX_HAMMING_DISTANCE,
    MIN_CONSECUTIVE_MATCHES,
)

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
    
    activity.logger.info(
        f"📥 [DOWNLOAD] fetch_event_data | fixture={fixture_id} | event={event_id}"
    )
    
    # Get fixture
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        activity.logger.error(
            f"❌ [DOWNLOAD] Fixture not found | fixture={fixture_id}"
        )
        return {"status": "error", "error": "fixture_not_found"}
    
    # Find event
    event = None
    for evt in fixture.get("events", []):
        if evt.get(EventFields.EVENT_ID) == event_id:
            event = evt
            break
    
    if not event:
        activity.logger.error(
            f"❌ [DOWNLOAD] Event not found | fixture={fixture_id} | event={event_id}"
        )
        return {"status": "error", "error": "event_not_found"}
    
    discovered_videos = event.get(EventFields.DISCOVERED_VIDEOS, [])
    if not discovered_videos:
        activity.logger.warning(
            f"⚠️ [DOWNLOAD] No discovered videos | event={event_id}"
        )
        return {"status": "no_videos", "discovered_videos": []}
    
    player_name = event.get("player", {}).get("name", "Unknown")
    team_name = event.get("team", {}).get("name", "Unknown")
    assister_name = event.get("assist", {}).get("player", {}).get("name", "")
    
    # Get existing videos from MongoDB (new _s3_videos schema)
    # MongoDB is the source of truth - contains full metadata (S3 metadata may be truncated)
    existing_s3_videos_mongo = event.get(EventFields.S3_VIDEOS, [])
    
    # Build existing video list from MongoDB data only (no S3 calls needed)
    existing_s3_videos = []  # List of video metadata for dedup comparison
    already_downloaded_urls = set()
    
    for video_obj in existing_s3_videos_mongo:
        s3_url = video_obj.get("url", "")
        if not s3_url:
            continue
        
        # Extract S3 key from URL or use stored key
        s3_key = video_obj.get("_s3_key", "")
        if not s3_key and s3_url.startswith("/video/footy-videos/"):
            s3_key = s3_url.replace("/video/footy-videos/", "")
        
        # Track source URL to skip already-downloaded videos
        source_url = video_obj.get("source_url", "")
        if source_url:
            already_downloaded_urls.add(source_url)
        
        # Use MongoDB data directly - it has the full untruncated metadata
        video_info = {
            "s3_url": s3_url,
            "s3_key": s3_key,
            "perceptual_hash": video_obj.get("perceptual_hash", ""),  # Full hash from MongoDB
            "width": video_obj.get("width", 0),
            "height": video_obj.get("height", 0),
            "bitrate": video_obj.get("bitrate", 0),
            "file_size": video_obj.get("file_size", 0),
            "source_url": source_url,
            "duration": video_obj.get("duration", 0),
            "resolution_score": video_obj.get("resolution_score", 0),
            "popularity": video_obj.get("popularity", 1),
        }
        existing_s3_videos.append(video_info)
        activity.logger.debug(f"✓ Existing video: {s3_key} ({video_info['width']}x{video_info['height']}, pop={video_info['popularity']})")
    
    # Filter discovered_videos to only NEW ones (URLs not already downloaded)
    videos_to_download = []
    for video in discovered_videos:
        video_url = video.get("tweet_url") or video.get("video_page_url")
        if video_url not in already_downloaded_urls:
            videos_to_download.append(video)
        else:
            activity.logger.debug(
                f"⏭️ [DOWNLOAD] Skipping already downloaded | url={video_url[:50]}..."
            )
    
    if not videos_to_download:
        activity.logger.info(
            f"⏭️ [DOWNLOAD] No new videos | event={event_id} | "
            f"all_already_in_s3={len(discovered_videos)}"
        )
        return {
            "status": "no_videos",
            "discovered_videos": [],
            "event": event,
            "existing_s3_videos": existing_s3_videos,
        }
    
    if existing_s3_videos:
        activity.logger.info(
            f"🔍 [DOWNLOAD] Cross-retry comparison | existing_s3={len(existing_s3_videos)}"
        )
    
    if len(videos_to_download) < len(discovered_videos):
        activity.logger.info(
            f"📥 [DOWNLOAD] Found new videos | event={event_id} | "
            f"to_download={len(videos_to_download)} | already_in_s3={len(discovered_videos) - len(videos_to_download)}"
        )
    else:
        activity.logger.info(
            f"📥 [DOWNLOAD] Found videos | event={event_id} | count={len(videos_to_download)}"
        )
    
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
    Download video(s) from a tweet with yt-dlp, extract metadata, and calculate hash.
    
    Handles multi-video tweets by downloading ALL videos and returning results for each.
    Returns a list of results in the "videos" field when multiple videos are found.
    
    Args:
        video_url: URL to download (video page URL from Twitter)
        video_index: Index of this video in the list (used for filename prefix)
        event_id: Event ID for filename
        temp_dir: Temporary directory to download to
        source_tweet_url: Original tweet URL (for dedup checking)
    
    Returns:
        Dict with status and either single video info or "videos" list for multi-video tweets
    
    Raises:
        Exception: If download fails (for Temporal retry)
    """
    import subprocess
    import time
    import json
    import glob
    
    # Ensure temp directory exists (activity can do I/O)
    os.makedirs(temp_dir, exist_ok=True)
    
    # Output template handles multi-video tweets with playlist_index
    # For single video: event_id_0_01.mp4
    # For multi-video: event_id_0_01.mp4, event_id_0_02.mp4, etc.
    output_template = os.path.join(temp_dir, f"{event_id}_{video_index}_%(playlist_index)02d.mp4")
    
    activity.logger.info(
        f"📥 [DOWNLOAD] Starting download | event={event_id} | video_idx={video_index} | "
        f"url={video_url[:60]}..."
    )
    
    # Use shared cookie file from /config mount
    cookies_json_file = "/config/twitter_cookies.json"
    
    if not os.path.exists(cookies_json_file):
        msg = f"[DOWNLOAD] No cookies found | path={cookies_json_file}"
        activity.logger.error(f"❌ {msg}")
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
        
        # Run yt-dlp - downloads ALL videos from multi-video tweets
        result = subprocess.run([
            'yt-dlp',
            '--cookies', temp_cookie_netscape,
            '--format', 'best[ext=mp4]/best',
            '--output', output_template,
            '--no-warnings',
            '--quiet',
            '--socket-timeout', '15',
            '--retries', '1',
            video_url
        ], capture_output=True, text=True, timeout=60)  # Increased timeout for multi-video
        
        # Cleanup temp cookie file
        try:
            os.remove(temp_cookie_netscape)
        except:
            pass
        
        if result.returncode != 0:
            error_msg = result.stderr[:200] if result.stderr else "Unknown error"
            activity.logger.warning(
                f"⚠️ [DOWNLOAD] yt-dlp failed | video_idx={video_index} | error={error_msg}"
            )
            raise RuntimeError(f"yt-dlp failed: {error_msg}")
            
    except subprocess.TimeoutExpired:
        activity.logger.warning(
            f"⚠️ [DOWNLOAD] Timeout | video_idx={video_index} | url={video_url[:50]}..."
        )
        raise RuntimeError("Download timed out")
    except Exception as e:
        activity.logger.warning(
            f"⚠️ [DOWNLOAD] Failed | video_idx={video_index} | error={str(e)[:100]}"
        )
        raise
    
    # Find all downloaded files (handles multi-video tweets)
    pattern = os.path.join(temp_dir, f"{event_id}_{video_index}_*.mp4")
    downloaded_files = sorted(glob.glob(pattern))
    
    if not downloaded_files:
        msg = f"[DOWNLOAD] No files after yt-dlp | video_idx={video_index}"
        activity.logger.error(f"❌ {msg}")
        raise RuntimeError(msg)
    
    if len(downloaded_files) > 1:
        activity.logger.info(
            f"📹 [DOWNLOAD] Multi-video tweet | count={len(downloaded_files)}"
        )
    
    # Process each downloaded file
    results = []
    for sub_idx, output_path in enumerate(downloaded_files):
        sub_result = _process_downloaded_video(
            output_path, 
            video_index, 
            sub_idx, 
            source_tweet_url,
            len(downloaded_files) > 1  # is_multi_video
        )
        if sub_result:
            results.append(sub_result)
    
    # Return based on number of results
    if len(results) == 0:
        # All videos were filtered
        return {"status": "filtered", "reason": "all_filtered", "source_url": source_tweet_url}
    elif len(results) == 1:
        # Single video (most common case)
        return results[0]
    else:
        # Multi-video tweet - return list
        activity.logger.info(
            f"✅ [DOWNLOAD] Multi-video passed filters | count={len(results)}"
        )
        return {"status": "multi_video", "videos": results, "source_url": source_tweet_url}


def _process_downloaded_video(
    output_path: str,
    video_index: int,
    sub_index: int,
    source_tweet_url: str,
    is_multi_video: bool = False
) -> Optional[Dict[str, Any]]:
    """
    Process a single downloaded video file - get metadata and apply basic filters.
    
    NOTE: Does NOT generate perceptual hash here. Hash generation happens AFTER
    AI validation to avoid wasting compute on non-soccer videos.
    
    Args:
        output_path: Path to the downloaded video file
        video_index: Main video index
        sub_index: Sub-index within multi-video tweet (0 for single video)
        source_tweet_url: Original tweet URL
        is_multi_video: Whether this is from a multi-video tweet
        
    Returns:
        Dict with video info (no perceptual_hash yet) or None if filtered out
    """
    from temporalio import activity
    
    display_idx = f"{video_index}.{sub_index}" if is_multi_video else str(video_index)
    
    # Get video metadata (duration, resolution, bitrate)
    video_meta = _get_video_metadata(output_path)
    duration = video_meta["duration"]
    width = video_meta["width"]
    height = video_meta["height"]
    bitrate = video_meta["bitrate"]
    
    # Duration filter: typical goal clips
    # Must be strictly greater than MIN (e.g., exactly 3.00s fails)
    if duration <= MIN_VIDEO_DURATION:
        activity.logger.warning(
            f"⏱️ [DOWNLOAD] Filtered: too short | video={display_idx} | "
            f"duration={duration:.1f}s | min={MIN_VIDEO_DURATION}s"
        )
        os.remove(output_path)
        return None
    
    if duration > MAX_VIDEO_DURATION:
        activity.logger.warning(
            f"⏱️ [DOWNLOAD] Filtered: too long | video={display_idx} | "
            f"duration={duration:.1f}s | max={MAX_VIDEO_DURATION}s"
        )
        os.remove(output_path)
        return None
    
    # Aspect ratio filter: reject portrait/square videos (< 4:3)
    # Disabled by default to allow stadium phone recordings
    # Phone-TV recordings are filtered by AI vision instead
    if ASPECT_RATIO_FILTER_ENABLED and width and height and height > 0:
        aspect_ratio = width / height
        if aspect_ratio < MIN_ASPECT_RATIO:
            activity.logger.warning(
                f"📐 [DOWNLOAD] Filtered: aspect ratio | video={display_idx} | "
                f"ratio={aspect_ratio:.2f} | min={MIN_ASPECT_RATIO}"
            )
            os.remove(output_path)
            return None
    
    # Calculate MD5 hash and size (fast)
    file_hash = _calculate_md5(output_path)
    file_size = os.path.getsize(output_path)
    
    quality_info = f"{width}x{height}" if width and height else "unknown res"
    if bitrate:
        quality_info += f"@{bitrate:.0f}kbps"
    
    activity.logger.info(
        f"✅ [DOWNLOAD] Video ready for validation | video={display_idx} | "
        f"size={file_size / 1024 / 1024:.2f}MB | duration={duration:.1f}s | quality={quality_info}"
    )
    
    # Return without perceptual_hash - will be generated after AI validation
    return {
        "status": "success",
        "file_path": output_path,
        "file_hash": file_hash,
        "perceptual_hash": "",  # Placeholder - generated after AI validation
        "duration": duration,
        "file_size": file_size,
        "video_index": video_index,
        "sub_index": sub_index,
        "source_url": source_tweet_url,
        "width": width,
        "height": height,
        "bitrate": bitrate,
        "resolution_score": width * height if width and height else 0,
    }


@activity.defn
async def deduplicate_by_md5(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None
) -> Dict[str, Any]:
    """
    Fast MD5-based deduplication for TRUE duplicates (identical files).
    
    This runs BEFORE AI validation and perceptual hashing to eliminate
    exact duplicates early, saving expensive compute.
    
    MD5 hash comparison is O(1) per comparison vs perceptual hash which is O(frames).
    
    Two-phase deduplication:
    1. BATCH DEDUP: Group by file_hash, keep highest quality from each group
    2. S3 DEDUP: Check if file_hash matches any existing S3 video filename
    
    Args:
        downloaded_files: List of download results with file_path, file_hash, file_size, etc.
        existing_s3_videos: List of existing S3 video metadata from fetch_event_data
    
    Returns:
        Dict with:
        - unique_videos: Videos that survived MD5 dedup (need AI validation + perceptual hash)
        - md5_duplicates_removed: Count of duplicates eliminated
        - s3_matches: Videos that match existing S3 (bump popularity or replace)
    """
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    
    if not successful:
        return {
            "unique_videos": [],
            "md5_duplicates_removed": 0,
            "s3_exact_matches": [],
            "s3_replacements": [],
        }
    
    activity.logger.info(
        f"🔐 [MD5-DEDUP] Starting fast MD5 deduplication | videos={len(successful)}"
    )
    
    # =========================================================================
    # PHASE 1: BATCH DEDUP - Group by MD5 hash, keep best quality from each
    # =========================================================================
    md5_groups = {}  # file_hash -> list of videos with that hash
    
    for video in successful:
        file_hash = video.get("file_hash", "")
        if not file_hash:
            # No hash - can't dedup, keep it
            activity.logger.warning(f"⚠️ [MD5-DEDUP] Video has no MD5 hash, keeping")
            if "" not in md5_groups:
                md5_groups[""] = []
            md5_groups[""].append(video)
            continue
        
        if file_hash not in md5_groups:
            md5_groups[file_hash] = []
        md5_groups[file_hash].append(video)
    
    # For each group, keep the best quality video
    batch_winners = []
    batch_duplicates_removed = 0
    files_to_delete = []
    
    for file_hash, group in md5_groups.items():
        if len(group) == 1:
            # Single video with this hash - keep it
            group[0]["popularity"] = 1
            batch_winners.append(group[0])
        else:
            # Multiple videos with same MD5 - TRUE DUPLICATES (identical content)
            # Keep the one with best resolution, accumulate popularity
            best = max(group, key=lambda v: (v.get("resolution_score", 0), v.get("file_size", 0)))
            best["popularity"] = len(group)  # All duplicates contribute to popularity
            batch_winners.append(best)
            batch_duplicates_removed += len(group) - 1
            
            activity.logger.info(
                f"🗑️ [MD5-DEDUP] Found {len(group)} identical files (MD5={file_hash[:8]}), "
                f"keeping best ({best.get('width', 0)}x{best.get('height', 0)})"
            )
            
            # Mark other files for deletion
            for video in group:
                if video["file_path"] != best["file_path"]:
                    files_to_delete.append(video["file_path"])
    
    # Delete duplicate files
    for file_path in files_to_delete:
        try:
            import os
            os.remove(file_path)
        except:
            pass
    
    # =========================================================================
    # PHASE 2: S3 DEDUP - Check MD5 against existing S3 video filenames
    # S3 filenames are: {event_id}_{md5[:8]}.mp4
    # =========================================================================
    s3_exact_matches = []  # Videos that exactly match S3 (same MD5)
    s3_replacements = []   # Videos that should replace S3 (same MD5, better quality)
    unique_videos = []     # Videos with no S3 match
    
    existing_s3_list = existing_s3_videos or []
    
    # Build map of existing S3 video MD5 hashes (extracted from filename)
    existing_md5_to_s3 = {}  # md5_prefix -> s3_video_info
    for s3_video in existing_s3_list:
        s3_key = s3_video.get("s3_key", "") or s3_video.get("s3_url", "").replace("/video/footy-videos/", "")
        if s3_key:
            # Extract MD5 prefix from filename: event_id_{md5[:8]}.mp4
            filename = s3_key.split("/")[-1]  # Get just the filename
            if "_" in filename and filename.endswith(".mp4"):
                # Format: eventid_hash.mp4 -> extract hash
                parts = filename[:-4].split("_")  # Remove .mp4 and split
                if parts:
                    md5_prefix = parts[-1]  # Last part should be the MD5 prefix
                    if len(md5_prefix) == 8:  # Valid MD5 prefix
                        existing_md5_to_s3[md5_prefix] = s3_video
    
    for video in batch_winners:
        file_hash = video.get("file_hash", "")
        md5_prefix = file_hash[:8] if file_hash else ""
        
        if md5_prefix and md5_prefix in existing_md5_to_s3:
            # Found MD5 match in S3!
            existing_s3 = existing_md5_to_s3[md5_prefix]
            existing_file_size = existing_s3.get("file_size", 0)
            existing_resolution = existing_s3.get("resolution_score", 0)
            existing_popularity = existing_s3.get("popularity", 1)
            
            new_file_size = video.get("file_size", 0)
            new_resolution = video.get("resolution_score", 0)
            new_popularity = video.get("popularity", 1)
            
            # Check if new is better quality
            is_better = (new_resolution > existing_resolution) or \
                       (new_resolution == existing_resolution and new_file_size > existing_file_size)
            
            if is_better:
                # Replace S3 video with better quality
                video["popularity"] = existing_popularity + new_popularity
                s3_replacements.append({
                    "new_video": video,
                    "old_s3_video": existing_s3,
                })
                activity.logger.info(
                    f"🔄 [MD5-DEDUP] S3 replacement | md5={md5_prefix} | "
                    f"new={new_resolution} > existing={existing_resolution}"
                )
            else:
                # Existing S3 is same/better - just bump popularity
                s3_exact_matches.append({
                    "video": video,
                    "s3_video": existing_s3,
                    "new_popularity": existing_popularity + new_popularity,
                })
                activity.logger.info(
                    f"⏭️ [MD5-DEDUP] S3 match (keeping existing) | md5={md5_prefix} | "
                    f"bumping popularity {existing_popularity} → {existing_popularity + new_popularity}"
                )
                # Delete local file - not needed
                try:
                    import os
                    os.remove(video["file_path"])
                except:
                    pass
        else:
            # No S3 match - needs further processing
            unique_videos.append(video)
    
    activity.logger.info(
        f"✅ [MD5-DEDUP] Complete | unique={len(unique_videos)} | "
        f"batch_dupes_removed={batch_duplicates_removed} | "
        f"s3_matches={len(s3_exact_matches)} | s3_replacements={len(s3_replacements)}"
    )
    
    return {
        "unique_videos": unique_videos,
        "md5_duplicates_removed": batch_duplicates_removed,
        "s3_exact_matches": s3_exact_matches,
        "s3_replacements": s3_replacements,
    }@activity.defn
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None
) -> Dict[str, Any]:
    """
    Smart deduplication that keeps BOTH longest AND largest videos.
    
    Two-phase deduplication:
    1. BATCH DEDUP FIRST: Group downloaded videos into duplicate clusters
    2. S3 DEDUP SECOND: Compare batch winners against existing S3 videos
    
    For each duplicate cluster, we keep:
    - The video with longest duration (more content/context)
    - The video with largest file size (higher quality)
    - If same video is both longest AND largest, keep just that one
    - If different videos, keep BOTH (best of both worlds)
    
    Popularity score accumulates from all duplicates to the keepers.
    
    Args:
        downloaded_files: List of download results with file_path, perceptual_hash, file_size, etc.
        existing_s3_videos: List of existing S3 video metadata from fetch_event_data
    
    Returns:
        Dict with:
        - videos_to_upload: List of unique videos to upload (no S3 match)
        - videos_to_replace: List of {new_video, old_s3_video} for replacement
        - videos_to_bump_popularity: List of {s3_video, new_popularity} for existing better videos
        - skipped_urls: List of source URLs that we're not uploading
    """
    # Filter out failed and filtered downloads
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    filtered_count = sum(1 for f in downloaded_files if f.get("status") == "filtered")
    
    if filtered_count > 0:
        activity.logger.info(
            f"🚫 [DEDUP] Pre-filtered by duration | count={filtered_count}"
        )
    
    if not successful:
        activity.logger.warning(
            f"⚠️ [DEDUP] No successful downloads to deduplicate"
        )
        return {"videos_to_upload": [], "videos_to_replace": [], "videos_to_bump_popularity": [], "skipped_urls": []}
    
    activity.logger.info(
        f"📥 [DEDUP] Starting deduplication | successful={len(successful)} | "
        f"existing_s3={len(existing_s3_videos) if existing_s3_videos else 0}"
    )
    
    # =========================================================================
    # PHASE 1: BATCH DEDUP - Group into duplicate clusters, keep longest+largest
    # =========================================================================
    
    # Build duplicate clusters using union-find approach
    clusters = []  # List of lists, each inner list is a cluster of duplicates
    videos_without_hash = 0  # Track videos that bypass dedup
    
    for file_info in successful:
        perceptual_hash = file_info["perceptual_hash"]
        
        # Track videos without valid hash - they can't be deduplicated!
        if not perceptual_hash or perceptual_hash == "dense:0.25:":
            videos_without_hash += 1
        
        # Find which cluster(s) this video matches
        matching_cluster_idx = None
        for idx, cluster in enumerate(clusters):
            for member in cluster:
                if _perceptual_hashes_match(perceptual_hash, member["perceptual_hash"]):
                    matching_cluster_idx = idx
                    break
            if matching_cluster_idx is not None:
                break
        
        if matching_cluster_idx is not None:
            clusters[matching_cluster_idx].append(file_info)
        else:
            # New cluster
            clusters.append([file_info])
    
    if videos_without_hash > 0:
        activity.logger.warning(
            f"⚠️ [DEDUP] Videos without hash | count={videos_without_hash}/{len(successful)} | "
            f"warning=cannot_deduplicate"
        )
    
    activity.logger.info(
        f"📊 [DEDUP] Clustered videos | clusters={len(clusters)} | from_downloads={len(successful)}"
    )
    
    # For each cluster, select winners (longest + largest, could be same video)
    batch_winners = []
    files_to_remove = []
    
    for cluster in clusters:
        if len(cluster) == 1:
            # Single video - preserve incoming popularity from MD5 dedup
            if "popularity" not in cluster[0] or cluster[0]["popularity"] < 1:
                cluster[0]["popularity"] = 1
            batch_winners.append(cluster[0])
            continue
        
        # Find longest duration video
        longest = max(cluster, key=lambda x: x.get("duration", 0))
        
        # Find largest file size video
        largest = max(cluster, key=lambda x: x.get("file_size", 0))
        
        # Accumulate popularity from ALL videos in cluster (including their MD5 dedup popularity)
        # This preserves popularity from earlier dedup phases
        total_popularity = sum(v.get("popularity", 1) for v in cluster)
        
        if longest["file_path"] == largest["file_path"]:
            # Same video is both longest AND largest - perfect!
            winner = longest
            winner["popularity"] = total_popularity
            batch_winners.append(winner)
            activity.logger.info(
                f"🏆 Cluster winner: {winner.get('duration', 0):.1f}s, {winner.get('file_size', 0):,} bytes "
                f"(both longest & largest, {len(cluster)} dups)"
            )
            # Remove other files in cluster
            for member in cluster:
                if member["file_path"] != winner["file_path"]:
                    files_to_remove.append(member["file_path"])
        else:
            # Different videos - keep BOTH
            # Split popularity evenly (no inflation - if a future video is both longest+largest it inherits both)
            longest_popularity = (total_popularity + 1) // 2  # Ceiling
            largest_popularity = total_popularity // 2  # Floor
            
            longest["popularity"] = longest_popularity
            largest["popularity"] = largest_popularity
            batch_winners.append(longest)
            batch_winners.append(largest)
            
            activity.logger.info(
                f"🏆 Cluster has 2 winners from {len(cluster)} videos (total_pop={total_popularity}):"
            )
            activity.logger.info(
                f"   📏 Longest: {longest.get('duration', 0):.1f}s, {longest.get('file_size', 0):,} bytes (pop={longest_popularity})"
            )
            activity.logger.info(
                f"   📦 Largest: {largest.get('duration', 0):.1f}s, {largest.get('file_size', 0):,} bytes (pop={largest_popularity})"
            )
            
            # Remove other files in cluster (not the two winners)
            for member in cluster:
                if member["file_path"] not in [longest["file_path"], largest["file_path"]]:
                    files_to_remove.append(member["file_path"])
    
    # Clean up discarded files
    for file_path in files_to_remove:
        try:
            os.remove(file_path)
        except Exception:
            pass
    
    activity.logger.info(f"📊 Batch dedup: {len(successful)} → {len(batch_winners)} keepers ({len(files_to_remove)} removed)")
    
    # =========================================================================
    # PHASE 2: S3 DEDUP - Compare batch winners against existing S3 videos
    # =========================================================================
    existing_videos_list = existing_s3_videos or []
    if existing_videos_list:
        activity.logger.info(f"📦 Comparing {len(batch_winners)} batch winners against {len(existing_videos_list)} S3 videos...")
    
    videos_to_upload = []  # New videos (no S3 match)
    videos_to_replace = []  # Higher quality than existing S3 video
    videos_to_bump_popularity = []  # Existing S3 is better
    skipped_urls = []  # URLs we're not uploading
    
    for file_info in batch_winners:
        perceptual_hash = file_info["perceptual_hash"]
        file_path = file_info["file_path"]
        file_size = file_info["file_size"]
        duration = file_info.get("duration", 0)
        source_url = file_info.get("source_url", "")
        incoming_popularity = file_info.get("popularity", 1)  # From local dedup
        
        # CRITICAL: Warn if video has no hash - dedup will NOT work!
        if not perceptual_hash or perceptual_hash == "dense:0.25:":
            activity.logger.warning(
                f"⚠️ Video has NO perceptual hash! Deduplication BYPASSED for {source_url} "
                f"- this video will be uploaded even if it's a duplicate!"
            )
        
        # Check against existing S3 videos
        matched_existing = None
        for existing in existing_videos_list:
            existing_hash = existing.get("perceptual_hash", "")
            if existing_hash and _perceptual_hashes_match(perceptual_hash, existing_hash):
                matched_existing = existing
                break
        
        if matched_existing:
            # Found match in S3 - decide whether to replace or skip
            new_file_size = file_size
            new_duration = duration
            existing_file_size = matched_existing.get("file_size", 0)
            existing_duration = matched_existing.get("duration", 0)
            existing_popularity = matched_existing.get("popularity", 1)
            
            # Replace if new is LONGER or LARGER
            should_replace = (new_duration > existing_duration) or (new_file_size > existing_file_size)
            
            if should_replace:
                new_popularity = existing_popularity + incoming_popularity
                reason = []
                if new_duration > existing_duration:
                    reason.append(f"longer ({new_duration:.1f}s > {existing_duration:.1f}s)")
                if new_file_size > existing_file_size:
                    reason.append(f"larger ({new_file_size:,} > {existing_file_size:,} bytes)")
                activity.logger.info(
                    f"🔄 S3 REPLACE: New is {' & '.join(reason)} "
                    f"- popularity {existing_popularity} + {incoming_popularity} = {new_popularity}"
                )
                file_info["popularity"] = new_popularity
                videos_to_replace.append({
                    "new_video": file_info,
                    "old_s3_video": matched_existing
                })
            else:
                # Existing S3 is better - skip but bump popularity
                new_popularity = existing_popularity + incoming_popularity
                activity.logger.info(
                    f"⏭️ S3 SKIP: Existing is better ({existing_duration:.1f}s, {existing_file_size:,} bytes) "
                    f"- bumping popularity {existing_popularity} → {new_popularity}"
                )
                videos_to_bump_popularity.append({
                    "s3_video": matched_existing,
                    "new_popularity": new_popularity
                })
                os.remove(file_path)
                if source_url:
                    skipped_urls.append(source_url)
        else:
            # No S3 match - this is a new video
            activity.logger.info(f"✨ NEW: No S3 match ({file_size:,} bytes, {duration:.1f}s)")
            videos_to_upload.append(file_info)
    
    # Log summary
    activity.logger.info(
        f"✅ Dedup complete: "
        f"{len(videos_to_upload)} new uploads, "
        f"{len(videos_to_replace)} S3 replacements, "
        f"{len(videos_to_bump_popularity)} popularity bumps, "
        f"{len(skipped_urls)} skipped"
    )
    
    return {
        "videos_to_upload": videos_to_upload,
        "videos_to_replace": videos_to_replace,
        "videos_to_bump_popularity": videos_to_bump_popularity,
        "skipped_urls": skipped_urls
    }


def _extract_frame_for_vision(file_path: str, timestamp: float) -> Optional[str]:
    """
    Extract a single frame from video and return as base64.
    
    Args:
        file_path: Path to video file
        timestamp: Timestamp in seconds to extract frame
        
    Returns:
        Base64-encoded PNG image or None if extraction fails
    """
    import subprocess
    import base64
    
    try:
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
            stderr=subprocess.PIPE,
            timeout=10
        )
        
        if result.returncode != 0 or not result.stdout:
            activity.logger.warning(
                f"⚠️ Frame extraction failed at {timestamp}s: "
                f"returncode={result.returncode}, stderr={result.stderr.decode()[:200] if result.stderr else 'none'}"
            )
            return None
        
        return base64.b64encode(result.stdout).decode('utf-8')
    except subprocess.TimeoutExpired:
        activity.logger.warning(f"⚠️ Frame extraction timed out at {timestamp}s")
        return None
    except Exception as e:
        activity.logger.warning(f"⚠️ Frame extraction error at {timestamp}s: {e}")
        return None


async def _call_vision_model(image_base64: str, prompt: str) -> Optional[Dict[str, Any]]:
    """
    Call vision LLM with an image using llama.cpp OpenAI-compatible API.
    
    Args:
        image_base64: Base64-encoded image
        prompt: Question to ask about the image
        
    Returns:
        Dict with response content or None if failed
    """
    import httpx
    
    llama_url = LLAMA_CHAT_URL
    
    activity.logger.debug(f"🔍 Calling vision model at {llama_url}")
    
    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            # OpenAI-compatible multimodal format for llama.cpp
            response = await client.post(
                f"{llama_url}/v1/chat/completions",
                json={
                    "messages": [
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": prompt},
                                {
                                    "type": "image_url",
                                    "image_url": {
                                        "url": f"data:image/jpeg;base64,{image_base64}"
                                    }
                                }
                            ]
                        }
                    ],
                    "max_tokens": 100,
                    "temperature": 0.1
                }
            )
            
            if response.status_code == 200:
                return response.json()
            else:
                activity.logger.warning(
                    f"⚠️ Vision model returned status {response.status_code}: {response.text[:200]}"
                )
                return None
    except httpx.ConnectError as e:
        activity.logger.error(f"❌ Cannot connect to LLM at {llama_url}: {e}")
        raise  # Propagate connection errors - should retry
    except httpx.TimeoutException as e:
        activity.logger.warning(f"⚠️ Vision model request timed out: {e}")
        raise  # Propagate timeouts - should retry
    except Exception as e:
        activity.logger.error(f"❌ Vision model error: {type(e).__name__}: {e}")
        raise  # Propagate all errors - let retry policy handle


@activity.defn
async def validate_video_is_soccer(file_path: str, event_id: str) -> Dict[str, Any]:
    """
    AI validation to check if video is soccer AND not a phone-TV recording.
    
    Uses vision LLM to analyze frames and determine:
    1. Is this a soccer/football video?
    2. Is this someone filming a TV screen with their phone?
    
    We WANT:
    - Direct broadcast recordings
    - Stadium/in-person phone recordings (fans filming live)
    
    We REJECT:
    - Non-soccer content (ads, interviews, other sports)
    - Phone recordings of TV screens (moiré patterns, bezels, glare)
    
    Detection criteria for SOCCER:
    - Soccer/football field (green pitch, white lines)
    - Players in match uniforms
    - Goal posts/nets
    - Match action/gameplay
    
    Detection criteria for SCREEN RECORDING (reject):
    - TV bezel/frame visible around edges
    - Moiré patterns (wavy interference from filming display)
    - Screen glare or reflections
    - Room/furniture visible around TV
    - Curved or tilted screen perspective
    
    Args:
        file_path: Local path to video file
        event_id: Event ID for logging
    
    Returns:
        Dict with:
        - is_valid: True if soccer AND not screen recording
        - is_soccer: True if soccer content detected
        - is_screen_recording: True if phone-TV recording detected
        - confidence, reason, checks_performed
    
    Raises:
        FileNotFoundError: If video file doesn't exist
    """
    # Verify file exists before validation
    if not os.path.exists(file_path):
        msg = f"❌ FATAL: Video file not found for validation: {file_path}"
        activity.logger.error(msg)
        raise FileNotFoundError(msg)
    
    # Get video duration for frame extraction
    duration = 0.0
    try:
        import subprocess
        probe_cmd = [
            "ffprobe", "-v", "error", "-show_entries",
            "format=duration", "-of", "csv=p=0", file_path
        ]
        result = subprocess.run(probe_cmd, capture_output=True, text=True, timeout=10)
        if result.returncode == 0 and result.stdout.strip():
            duration = float(result.stdout.strip())
    except Exception as e:
        activity.logger.warning(f"⚠️ Failed to get duration: {e}")
        duration = 10.0  # Assume 10s if can't read
    
    if duration < 1.0:
        # Too short to analyze
        return {
            "is_valid": True,
            "confidence": 0.5,
            "reason": "Video too short to analyze",
            "detected_features": [],
            "checks_performed": 0,
        }
    
    # Combined vision prompt for soccer detection AND phone-TV rejection
    prompt = """/no_think
Analyze this image and answer TWO questions:

1. SOCCER: Is this a soccer/football match video? This includes:
   - Live match footage (players on pitch)
   - Replays and slow-motion reviews
   - VAR (Video Assistant Referee) review footage with overlays
   - Official broadcast with graphics, scoreboards, picture-in-picture
   - Stadium/fan recording from the stands (real crowd, real environment)
   Answer YES if any of the above.

2. SCREEN: Is this someone filming a TV/monitor screen with their phone/camera?
   Look for these PHYSICAL artifacts of pointing a camera at a screen:
   - Moiré patterns (rainbow/wavy interference lines on the display)
   - Visible TV bezel/frame edges around the picture
   - Screen glare or reflections from room lighting
   - Tilted/angled perspective (not straight-on)
   - Visible room, furniture, or surroundings in frame
   - Pixelation from screen refresh rate mismatch
   
   NOTE: Broadcast overlays, VAR boxes, replay borders, or graphics are NOT screen recordings.
   Only answer YES if you see evidence of a physical camera filming a physical screen, notably with black borders.

Answer format (exactly):
SOCCER: YES or NO
SCREEN: YES or NO"""

    activity.logger.info(
        f"🔍 [VALIDATE] Starting AI vision validation | event={event_id} | "
        f"duration={duration:.1f}s | file={os.path.basename(file_path)}"
    )
    
    # =========================================================================
    # Smart 2-3 check strategy:
    # 1. Check 25% and 75% first
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks)
    # =========================================================================
    
    def parse_response(resp) -> tuple[bool, bool]:
        """Parse vision model response, returns (is_soccer, is_screen_recording)"""
        if not resp:
            return (False, False)
        
        # Handle llama.cpp OpenAI format
        if "choices" in resp:
            content = resp.get("choices", [{}])[0].get("message", {}).get("content", "").upper()
            # Check for skip indicator (vision not available)
            if "SKIP" in content:
                return (True, False)  # Fail open when vision is unavailable
            
            # Parse the two answers
            is_soccer = "SOCCER:YES" in content or "SOCCER: YES" in content
            is_screen = "SCREEN:YES" in content or "SCREEN: YES" in content
            
            # Fallback: if format not matched, look for keywords
            if "SOCCER:" not in content:
                is_soccer = "YES" in content and "SOCCER" in content
            if "SCREEN:" not in content:
                is_screen = "MOIRE" in content or "BEZEL" in content or "TV FRAME" in content
            
            return (is_soccer, is_screen)
        
        # Legacy Ollama format
        text = resp.get("response", "").strip().upper()
        is_soccer = "SOCCER:YES" in text or ("YES" in text and "SOCCER" in text)
        is_screen = "SCREEN:YES" in text
        return (is_soccer, is_screen)
    
    # Extract frames at 25% and 75%
    t_25 = duration * 0.25
    t_75 = duration * 0.75
    
    frame_25 = _extract_frame_for_vision(file_path, t_25)
    frame_75 = _extract_frame_for_vision(file_path, t_75)
    
    if not frame_25 and not frame_75:
        msg = f"❌ [VALIDATE] Failed to extract ANY frames | event={event_id} | file={file_path}"
        activity.logger.error(msg)
        raise RuntimeError(msg)  # Let retry policy handle - don't fail-open
    
    # =========================================================================
    # Smart 2-3 check strategy with heartbeats:
    # 1. Check 25% and 75% first (heartbeat after each)
    # 2. If both agree → use that result (2 checks)
    # 3. If they disagree → check 50% as tiebreaker (3 checks, heartbeat after)
    # =========================================================================
    checks_performed = 0
    soccer_25, screen_25 = None, None
    soccer_75, screen_75 = None, None
    
    if frame_25:
        activity.heartbeat(f"AI vision check 1/2 (25% frame)...")
        response_25 = await _call_vision_model(frame_25, prompt)
        checks_performed += 1
        soccer_25, screen_25 = parse_response(response_25)
        activity.logger.info(
            f"   📸 [VALIDATE] 25% check | SOCCER={'YES' if soccer_25 else 'NO'} | "
            f"SCREEN={'YES' if screen_25 else 'NO'}"
        )
    
    # Heartbeat before second check
    activity.heartbeat(f"AI vision check 2/2 (75% frame)...")
    
    # Check 75%
    if frame_75:
        response_75 = await _call_vision_model(frame_75, prompt)
        checks_performed += 1
        soccer_75, screen_75 = parse_response(response_75)
        activity.logger.info(
            f"   📸 [VALIDATE] 75% check | SOCCER={'YES' if soccer_75 else 'NO'} | "
            f"SCREEN={'YES' if screen_75 else 'NO'}"
        )
    
    # Determine soccer result
    if soccer_25 is None and soccer_75 is not None:
        is_soccer = soccer_75
        confidence = 0.7
        soccer_reason = "Single frame check (25% failed)"
    elif soccer_75 is None and soccer_25 is not None:
        is_soccer = soccer_25
        confidence = 0.7
        soccer_reason = "Single frame check (75% failed)"
    elif soccer_25 == soccer_75:
        is_soccer = soccer_25
        confidence = 0.95 if is_soccer else 0.90
        soccer_reason = f"Both checks agree: {'soccer' if is_soccer else 'not soccer'}"
        activity.logger.info(
            f"   ✓ [VALIDATE] Both frames agree | is_soccer={'YES' if is_soccer else 'NO'}"
        )
    else:
        # Disagreement - need tiebreaker at 50%
        activity.logger.info(f"   ⚖️ [VALIDATE] Disagreement, checking 50% tiebreaker...")
        activity.heartbeat("AI vision tiebreaker (50% frame)...")
        
        t_50 = duration * 0.50
        frame_50 = _extract_frame_for_vision(file_path, t_50)
        
        if frame_50:
            response_50 = await _call_vision_model(frame_50, prompt)
            checks_performed += 1
            soccer_50, screen_50 = parse_response(response_50)
            activity.logger.info(
                f"   📸 [VALIDATE] 50% tiebreaker | SOCCER={'YES' if soccer_50 else 'NO'} | "
                f"SCREEN={'YES' if screen_50 else 'NO'}"
            )
            
            # Count votes (2/3 majority)
            yes_votes = sum([soccer_25 or False, soccer_50, soccer_75 or False])
            is_soccer = yes_votes >= 2
            confidence = 0.85
            soccer_reason = f"Tiebreaker decided: {yes_votes}/3 votes for soccer"
            
            # Also factor in screen detection from 50%
            if screen_50:
                screen_25 = screen_25 or screen_50
                screen_75 = screen_75 or screen_50
        else:
            is_soccer = soccer_25 if soccer_25 is not None else False
            confidence = 0.6
            soccer_reason = "Tiebreaker failed, using first check"
    
    # Determine screen recording result (reject if ANY frame detects it)
    is_screen_recording = (screen_25 or False) or (screen_75 or False)
    
    # Final validation: must be soccer AND not a screen recording
    is_valid = is_soccer and not is_screen_recording
    
    if is_screen_recording:
        reason = "Rejected: phone recording of TV/screen detected"
        activity.logger.warning(
            f"📺 [VALIDATE] REJECTED phone-TV recording | event={event_id} | "
            f"checks={checks_performed}"
        )
    elif not is_soccer:
        reason = soccer_reason
        activity.logger.warning(
            f"❌ [VALIDATE] REJECTED not soccer | event={event_id} | "
            f"checks={checks_performed} | reason={soccer_reason}"
        )
    else:
        reason = soccer_reason
        activity.logger.info(
            f"✅ [VALIDATE] PASSED validation | event={event_id} | "
            f"checks={checks_performed} | confidence={confidence:.0%}"
        )
    
    return {
        "is_valid": is_valid,
        "confidence": confidence,
        "reason": reason,
        "is_soccer": is_soccer,
        "is_screen_recording": is_screen_recording,
        "detected_features": ["soccer_field"] if is_soccer else [],
        "checks_performed": checks_performed,
    }


@activity.defn
async def generate_video_hash(file_path: str, duration: float) -> Dict[str, Any]:
    """
    Generate perceptual hash for a video file.
    
    Called AFTER AI validation to avoid wasting compute on non-soccer videos.
    Dense sampling at 0.25s intervals with histogram equalization.
    
    Uses heartbeats every ~10 frames to signal progress to Temporal.
    This allows long videos to process without timeout as long as
    they're making progress (not hung).
    
    Args:
        file_path: Path to video file
        duration: Video duration in seconds
        
    Returns:
        Dict with perceptual_hash string
    """
    if not os.path.exists(file_path):
        activity.logger.error(
            f"❌ [HASH] File not found | path={file_path}"
        )
        return {"perceptual_hash": "", "error": "file_not_found"}
    
    activity.logger.info(
        f"🔐 [HASH] Starting hash generation | file={os.path.basename(file_path)} | "
        f"duration={duration:.1f}s"
    )
    
    # Pass heartbeat function to signal progress during long hash generation
    perceptual_hash = _generate_perceptual_hash(file_path, duration, heartbeat_fn=activity.heartbeat)
    
    # Log hash info and validate result
    if perceptual_hash.startswith("dense:"):
        parts = perceptual_hash.split(":", 2)
        if len(parts) >= 3 and parts[2]:
            frame_count = len(parts[2].split(","))
            if frame_count >= 3:
                activity.logger.info(
                    f"✅ [HASH] Generated hash | frames={frame_count} | interval=0.25s"
                )
            else:
                activity.logger.warning(
                    f"⚠️ [HASH] Low frame count | frames={frame_count} | "
                    f"duration={duration}s | file={os.path.basename(file_path)}"
                )
        else:
            activity.logger.error(
                f"❌ [HASH] No frames extracted | file={os.path.basename(file_path)} | "
                f"duration={duration}s"
            )
            return {"perceptual_hash": "", "error": "no_frames_extracted"}
    else:
        activity.logger.error(
            f"❌ [HASH] Invalid format | file={os.path.basename(file_path)} | "
            f"hash_prefix={perceptual_hash[:50]}"
        )
        return {"perceptual_hash": "", "error": "invalid_hash_format"}
    
    return {"perceptual_hash": perceptual_hash}


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
    
    # S3 metadata - useful tags for manual lookup (not for dedup - MongoDB is source of truth)
    # Note: S3 metadata has ~2KB limit and truncates values, so we only store simple fields here
    metadata = {
        "player_name": player_name,
        "team_name": team_name,
        "event_id": event_id,
        "fixture_id": str(fixture_id),
        "popularity": str(popularity),
        "source_url": source_url,
        "duration": str(duration),
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
        f"☁️ [UPLOAD] Starting S3 upload | event={event_id} | video_idx={video_index} | "
        f"quality={quality_info} | popularity={popularity} | key={s3_key}"
    )
    
    s3_store = FootyS3Store()
    s3_url = s3_store.upload_video(file_path, s3_key, metadata=metadata)
    
    if not s3_url:
        msg = f"[UPLOAD] S3 returned None | event={event_id} | video_idx={video_index}"
        activity.logger.error(f"❌ {msg}")
        raise RuntimeError(msg)
    
    activity.logger.info(
        f"✅ [UPLOAD] Uploaded to S3 | event={event_id} | video_idx={video_index} | url={s3_url}"
    )
    
    # Return video object for MongoDB storage
    # Store ALL metadata in MongoDB to avoid S3 metadata truncation issues
    resolution_score = width * height if width and height else 0
    aspect_ratio = width / height if width and height and height > 0 else 0
    return {
        "status": "success",
        "s3_url": s3_url,
        "perceptual_hash": perceptual_hash,
        "resolution_score": resolution_score,
        "popularity": popularity,
        # Full video object for MongoDB _s3_videos array
        # This is the source of truth - S3 metadata may be truncated
        "video_object": {
            "url": s3_url,
            "_s3_key": s3_key,  # For easy S3 operations
            "perceptual_hash": perceptual_hash,  # Full hash (no truncation)
            "resolution_score": resolution_score,
            "file_size": file_size,
            "popularity": popularity,
            "rank": 0,  # Will be recalculated
            # Quality metadata for dedup/comparison
            "width": width,
            "height": height,
            "aspect_ratio": round(aspect_ratio, 2),
            "bitrate": int(bitrate) if bitrate else 0,
            "duration": round(duration, 2),
            "source_url": source_url,
            "hash_version": HASH_VERSION,  # Track hash algorithm version
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
    
    activity.logger.info(
        f"💾 [COMPLETE] Saving download results | event={event_id} | video_count={len(video_objects)}"
    )
    
    # Save video objects to _s3_videos array
    success = store.add_videos_to_event(fixture_id, event_id, video_objects)
    
    if not success:
        activity.logger.warning(
            f"⚠️ [COMPLETE] Failed to update event | event={event_id}"
        )
    else:
        # Recalculate ranks after adding new videos
        activity.logger.info(
            f"📊 [COMPLETE] Recalculating video ranks | event={event_id}"
        )
        store.recalculate_video_ranks(fixture_id, event_id)
    
    # Cleanup temp directory
    if temp_dir and os.path.exists(temp_dir):
        shutil.rmtree(temp_dir)
        activity.logger.info(
            f"🧹 [COMPLETE] Cleaned up temp dir | path={temp_dir}"
        )
    
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
    
    activity.logger.info(
        f"🗑️ [REPLACE] Deleting old video | event={event_id} | key={old_s3_key}"
    )
    
    # Delete from S3
    s3_store = FootyS3Store()
    try:
        s3_store.s3_client.delete_object(Bucket="footy-videos", Key=old_s3_key)
        activity.logger.info(
            f"✅ [REPLACE] S3 delete successful | key={old_s3_key}"
        )
    except Exception as e:
        activity.logger.error(
            f"❌ [REPLACE] S3 delete failed | key={old_s3_key} | error={e}"
        )
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
            activity.logger.info(
                f"✅ [REPLACE] MongoDB updated | removed={old_s3_url}"
            )
        else:
            activity.logger.warning(
                f"⚠️ [REPLACE] Video not found in MongoDB | url={old_s3_url}"
            )
        return True
    except Exception as e:
        activity.logger.error(
            f"❌ [REPLACE] MongoDB update failed | event={event_id} | error={e}"
        )
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
    
    activity.logger.info(
        f"📈 [POPULARITY] Bumping video popularity | event={event_id} | new_pop={new_popularity}"
    )
    
    success = store.update_video_popularity(fixture_id, event_id, s3_url, new_popularity)
    
    if success:
        activity.logger.info(
            f"✅ [POPULARITY] Updated | event={event_id} | popularity={new_popularity}"
        )
    else:
        activity.logger.warning(
            f"⚠️ [POPULARITY] Update failed | event={event_id} | url={s3_url}"
        )
    
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


def _generate_perceptual_hash(file_path: str, duration: float, heartbeat_fn=None) -> str:
    """
    Generate perceptual hash using dense sampling for offset-tolerant matching.
    
    Uses difference hash (dHash) algorithm with histogram equalization:
    1. Sample frames every 0.25s throughout the video
    2. Apply histogram equalization to normalize contrast/brightness
    3. Resize to 9x8, convert to grayscale
    4. Compare adjacent pixels to create 64-bit hash per frame
    
    Dense sampling (every 0.25s) solves the offset problem:
    - Different clips of same goal often start at different times
    - All-pairs comparison finds matching frames regardless of offset
    - Histogram equalization handles color grading differences
    
    Format: "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
    Example: "dense:0.25:0.25=a3f8b2e1c9d4f5a2,0.50=1e3b8c7d9f2a4b1c,..."
    
    Also supports legacy format for backward compatibility.
    
    Args:
        file_path: Path to video file
        duration: Video duration in seconds
        heartbeat_fn: Optional function to call periodically to signal activity is alive
        
    Returns:
        Dense hash string with all frame hashes
    """
    import subprocess
    from PIL import Image, ImageOps
    import io
    
    interval = 0.25  # Sample every 0.25 seconds
    total_frames = int((duration - 0.3) / interval)
    
    # CRITICAL: Send heartbeat IMMEDIATELY before any processing starts
    # This prevents timeout when multiple videos compete for resources
    if heartbeat_fn:
        heartbeat_fn(f"Starting hash generation for {total_frames} frames")
    
    def extract_frame_hash_normalized(timestamp: float) -> str:
        """Extract frame at timestamp, normalize, and compute dHash"""
        try:
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
            
            if result.returncode != 0 or not result.stdout:
                return ""
            
            # Load image, convert to grayscale, apply histogram equalization
            img = Image.open(io.BytesIO(result.stdout))
            img = img.convert('L')  # Grayscale
            img = ImageOps.equalize(img)  # Normalize contrast/brightness
            img = img.resize((9, 8), Image.Resampling.LANCZOS)
            
            pixels = list(img.getdata())
            
            # Compute difference hash
            hash_bits = []
            for row in range(8):
                for col in range(8):
                    left = pixels[row * 9 + col]
                    right = pixels[row * 9 + col + 1]
                    hash_bits.append('1' if left < right else '0')
            
            hash_int = int(''.join(hash_bits), 2)
            return format(hash_int, '016x')
            
        except Exception as e:
            activity.logger.warning(f"⚠️ Failed to extract frame at {timestamp}s: {e}")
            return ""
    
    # Extract hashes at 0.25s intervals
    hashes = []
    t = interval
    frame_count = 0
    
    while t < duration - 0.3:  # Stop before last 0.3s to avoid end-of-file issues
        # Send heartbeat BEFORE each ffmpeg call to prevent timeout during resource contention
        # When multiple videos are processed in parallel, ffmpeg calls can be very slow
        if heartbeat_fn:
            heartbeat_fn(f"Processing frame {frame_count + 1}/{total_frames}")
        
        frame_hash = extract_frame_hash_normalized(t)
        if frame_hash:
            hashes.append(f"{t:.2f}={frame_hash}")
        t += interval
        frame_count += 1
    
    if not hashes:
        # Fallback: try at least one frame at 1s
        if heartbeat_fn:
            heartbeat_fn("Fallback: trying single frame at 1s")
        frame_hash = extract_frame_hash_normalized(1.0)
        if frame_hash:
            hashes.append(f"1.00={frame_hash}")
    
    # Format: "dense:<interval>:<hash_list>"
    hash_list = ",".join(hashes)
    return f"dense:{interval}:{hash_list}"


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
    max_hamming: int = MAX_HAMMING_DISTANCE,
    min_consecutive_matches: int = MIN_CONSECUTIVE_MATCHES
) -> bool:
    """
    Check if two perceptual hashes represent the same video.
    
    Requires multiple CONSECUTIVE frames to match to avoid false positives
    from videos with similar content (e.g., goals 1 minute apart).
    
    Supports two formats:
    1. Dense format (new): "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
       - Uses sliding window to find consecutive matching frames
       - Requires min_consecutive_matches frames in a row to match
       
    2. Legacy format: "hash1:hash2:hash3" or "duration:hash1:hash2:hash3"
       - Fixed timestamp comparison (backward compatible)
       - Requires 2 of 3 frames to match
    
    Args:
        hash_a: First hash (dense or legacy format)
        hash_b: Second hash (dense or legacy format)
        max_hamming: Max hamming distance for a frame match (from config)
        min_consecutive_matches: Min consecutive frames that must match (from config)
        
    Returns:
        True if videos match (have consecutive matching frames)
    """
    # Early return for empty hashes (videos without hashes can't be compared)
    if not hash_a or not hash_b:
        return False
    
    try:
        # Check if both are dense format
        is_dense_a = hash_a.startswith("dense:")
        is_dense_b = hash_b.startswith("dense:")
        
        if is_dense_a and is_dense_b:
            # Dense format: require consecutive frames to match
            return _dense_hashes_match(hash_a, hash_b, max_hamming, min_consecutive_matches)
        
        # Legacy format or mixed: use simple matching
        hashes_a = _parse_perceptual_hash(hash_a)
        hashes_b = _parse_perceptual_hash(hash_b)
        
        if not hashes_a or not hashes_b:
            return False
        
        # For legacy format (3 hashes at 25%, 50%, 75%), require 2 of 3 to match
        matches = 0
        for h_a in hashes_a:
            for h_b in hashes_b:
                dist = _hamming_distance(h_a, h_b)
                if dist <= max_hamming:
                    matches += 1
                    break
        
        # Legacy: 2 of 3 frames must match
        return matches >= 2
        
    except Exception:
        return False


def _dense_hashes_match(
    hash_a: str,
    hash_b: str,
    max_hamming: int = MAX_HAMMING_DISTANCE,
    min_consecutive: int = MIN_CONSECUTIVE_MATCHES
) -> bool:
    """
    Check if two dense perceptual hashes match with consecutive frame requirement.
    
    For true duplicates (possibly with different start times), consecutive frames
    in video A should match frames in video B with a CONSISTENT time offset.
    
    Algorithm:
    1. For each possible time offset between A and B
    2. Count how many consecutive frames match at that offset
    3. If any offset has >= min_consecutive matches, videos are duplicates
    
    Args:
        hash_a: Dense hash "dense:<interval>:<ts1>=<hash1>,..."
        hash_b: Dense hash "dense:<interval>:<ts2>=<hash2>,..."
        max_hamming: Max hamming distance for frame match
        min_consecutive: Min consecutive frames required
        
    Returns:
        True if videos have consecutive matching frames at consistent offset
    """
    try:
        # Parse dense hashes into {timestamp: hash_int}
        def parse_dense(h):
            parts = h.split(":", 2)
            if len(parts) < 3:
                return {}, 0.25
            interval = float(parts[1])
            frames = {}
            for pair in parts[2].split(","):
                if "=" in pair:
                    ts_str, hash_hex = pair.split("=", 1)
                    frames[float(ts_str)] = int(hash_hex, 16)
            return frames, interval
        
        frames_a, interval_a = parse_dense(hash_a)
        frames_b, interval_b = parse_dense(hash_b)
        
        if len(frames_a) < min_consecutive or len(frames_b) < min_consecutive:
            return False
        
        timestamps_a = sorted(frames_a.keys())
        timestamps_b = sorted(frames_b.keys())
        
        # Try each possible starting alignment between A and B
        # For each frame in A, try aligning it with each frame in B
        for start_a in timestamps_a:
            for start_b in timestamps_b:
                offset = start_b - start_a  # Time offset: B = A + offset
                
                # Count consecutive matches at this offset
                consecutive = 0
                max_consecutive = 0
                
                for ts_a in timestamps_a:
                    ts_b = ts_a + offset
                    
                    # Find closest timestamp in B (within tolerance)
                    tolerance = interval_a / 2
                    matched = False
                    
                    for actual_ts_b in timestamps_b:
                        if abs(actual_ts_b - ts_b) <= tolerance:
                            h_a = frames_a[ts_a]
                            h_b = frames_b[actual_ts_b]
                            dist = bin(h_a ^ h_b).count('1')
                            if dist <= max_hamming:
                                matched = True
                                break
                    
                    if matched:
                        consecutive += 1
                        max_consecutive = max(max_consecutive, consecutive)
                        if max_consecutive >= min_consecutive:
                            return True
                    else:
                        consecutive = 0
        
        return False
        
    except Exception:
        return False


def _get_duration_from_hash(hash_str: str) -> float:
    """
    Extract video duration from a perceptual hash string.
    
    For dense format, duration is the last timestamp.
    For legacy format, duration was the first part (if 4 parts).
    
    Returns:
        Duration in seconds, or 0.0 if unable to parse.
    """
    if not hash_str:
        return 0.0
    
    try:
        if hash_str.startswith("dense:"):
            # Dense format: "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
            parts = hash_str.split(":", 2)
            if len(parts) < 3:
                return 0.0
            
            # Get the last timestamp
            hash_list = parts[2]
            items = hash_list.split(",")
            if items:
                last_item = items[-1]
                if "=" in last_item:
                    ts_str, _ = last_item.split("=", 1)
                    return float(ts_str)
        else:
            # Legacy format: "duration:hash1:hash2:hash3" 
            parts = hash_str.split(":")
            if len(parts) == 4:
                # Old format with duration prefix
                try:
                    return float(parts[0])
                except ValueError:
                    pass
        return 0.0
    except Exception:
        return 0.0


def _parse_perceptual_hash(hash_str: str) -> list:
    """
    Parse a perceptual hash string into a list of frame hashes.
    
    Supports:
    - Dense format: "dense:0.25:0.25=abc123,0.50=def456,..."
    - Legacy format: "hash1:hash2:hash3" or "duration:hash1:hash2:hash3"
    
    Returns:
        List of hex hash strings
    """
    if not hash_str:
        return []
    
    if hash_str.startswith("dense:"):
        # Dense format: "dense:<interval>:<ts1>=<hash1>,<ts2>=<hash2>,..."
        parts = hash_str.split(":", 2)
        if len(parts) < 3:
            return []
        
        hash_list = parts[2]
        hashes = []
        for item in hash_list.split(","):
            if "=" in item:
                _, frame_hash = item.split("=", 1)
                if frame_hash:
                    hashes.append(frame_hash)
        return hashes
    else:
        # Legacy format: "hash1:hash2:hash3" or "duration:hash1:hash2:hash3"
        parts = hash_str.split(":")
        if len(parts) == 4:
            # Old format with duration prefix - skip it
            return parts[1:4]
        elif len(parts) == 3:
            # New legacy format without duration
            return parts
        else:
            return []


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