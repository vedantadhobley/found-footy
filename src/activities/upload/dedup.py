"""
Upload dedup activities — MD5 batch dedup + perceptual hash dedup (scoped).

Companion modules:
  - upload/core.py   — the upload pipeline + Mongo writes + cleanup
  - src/utils/dedup_match.py — pure matching helpers

The two activities here are the heaviest in the upload pipeline (perceptual
dedup runs all-pairs comparison with heartbeat-bounded execution). They
are split into a separate module so the matching primitives in
`dedup_match` can be evolved (or deleted under Phase 5) without touching
the Temporal activity wrappers.
"""

from temporalio import activity
from typing import Any, Dict, List, Optional
import os

from src.data.models import EventFields
from src.utils.footy_logging import log
from src.utils.config import (
    HASH_VERSION,
    MAX_HAMMING_DISTANCE,
    MIN_CONSECUTIVE_MATCHES,
)
from src.utils.dedup_match import (
    _hamming_distance,
    _perceptual_hashes_match,
    _dense_hashes_match,
    _parse_perceptual_hash,
)

MODULE = "upload"


@activity.defn
async def deduplicate_by_md5(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None,
    event_id: str = "",
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
        - md5_duplicates_removed: Count of removed duplicates
        - s3_exact_matches: Videos matching S3 by MD5 (for popularity bump)
        - s3_replacements: Videos that should replace S3 (same MD5, better quality)
    """
    # Filter to successful downloads only
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    
    if not successful:
        log.info(activity.logger, MODULE, "md5_dedup_no_downloads", "No successful downloads to deduplicate",
                 event_id=event_id)
        return {
            "unique_videos": [],
            "md5_duplicates_removed": 0,
            "s3_exact_matches": [],
            "s3_replacements": [],
        }
    
    log.info(activity.logger, MODULE, "md5_dedup_started", "Starting MD5 deduplication",
             event_id=event_id, downloads=len(successful), existing_s3=len(existing_s3_videos) if existing_s3_videos else 0)
    
    # =========================================================================
    # PHASE 1: BATCH DEDUP - Group by MD5 hash, keep best quality from each group
    # =========================================================================
    md5_groups: Dict[str, List[Dict]] = {}
    for video in successful:
        file_hash = video.get("file_hash", "")
        if file_hash:
            if file_hash not in md5_groups:
                md5_groups[file_hash] = []
            md5_groups[file_hash].append(video)
    
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
            
            log.info(activity.logger, MODULE, "md5_identical_found", "Found identical files",
                     event_id=event_id, count=len(group), md5_prefix=file_hash[:8], 
                     winner_width=best.get('width', 0), winner_height=best.get('height', 0))
            
            # Mark other files for deletion
            for video in group:
                if video["file_path"] != best["file_path"]:
                    files_to_delete.append(video["file_path"])
    
    # Delete duplicate files
    delete_failures = 0
    for file_path in files_to_delete:
        try:
            os.remove(file_path)
        except Exception as e:
            delete_failures += 1
            log.warning(activity.logger, MODULE, "delete_file_failed", "Failed to delete duplicate file",
                        event_id=event_id, file_path=file_path, error=str(e)[:100])
    
    if delete_failures > 0:
        log.warning(activity.logger, MODULE, "batch_delete_failures", f"{delete_failures} file deletions failed",
                    event_id=event_id, failed=delete_failures, total=len(files_to_delete))
    
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
                log.info(activity.logger, MODULE, "md5_s3_replacement", "S3 replacement found",
                         event_id=event_id, md5_prefix=md5_prefix, new_resolution=new_resolution,
                         existing_resolution=existing_resolution)
            else:
                # Existing S3 is same/better - just bump popularity
                s3_exact_matches.append({
                    "video": video,
                    "s3_video": existing_s3,
                    "new_popularity": existing_popularity + new_popularity,
                })
                log.info(activity.logger, MODULE, "md5_s3_match", "S3 match - keeping existing",
                         event_id=event_id, md5_prefix=md5_prefix, old_popularity=existing_popularity,
                         new_popularity=existing_popularity + new_popularity)
                # Delete local file - not needed
                try:
                    os.remove(video["file_path"])
                    log.debug(activity.logger, MODULE, "local_file_deleted", "Deleted local file after S3 match",
                              event_id=event_id, file_path=video["file_path"])
                except Exception as e:
                    log.warning(activity.logger, MODULE, "delete_after_s3_match_failed", 
                                "Failed to delete local file after S3 match",
                                event_id=event_id, file_path=video["file_path"], error=str(e)[:100])
        else:
            # No S3 match - needs further processing
            unique_videos.append(video)
    
    log.info(activity.logger, MODULE, "md5_dedup_complete", "MD5 deduplication complete",
             event_id=event_id, unique=len(unique_videos), batch_dupes_removed=batch_duplicates_removed,
             s3_matches=len(s3_exact_matches), s3_replacements=len(s3_replacements))
    
    return {
        "unique_videos": unique_videos,
        "md5_duplicates_removed": batch_duplicates_removed,
        "s3_exact_matches": s3_exact_matches,
        "s3_replacements": s3_replacements,
    }

@activity.defn
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None,
    event_id: str = "",
) -> Dict[str, Any]:
    """
    Smart deduplication that keeps BOTH longest AND largest videos.
    
    Two-phase deduplication:
    1. BATCH DEDUP FIRST: Group downloaded videos into duplicate clusters
    2. S3 DEDUP: Compare batch winners against existing S3 videos for THIS event
    
    For each duplicate cluster, we keep the single best video based on (duration, file_size).
    Popularity score accumulates from all duplicates to the keepers.
    
    Args:
        downloaded_files: List of download results with file_path, perceptual_hash, file_size, etc.
        existing_s3_videos: List of existing S3 video metadata for THIS event
    
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
        log.info(activity.logger, MODULE, "dedup_prefiltered", "Pre-filtered by duration",
                 event_id=event_id, count=filtered_count)
    
    if not successful:
        log.warning(activity.logger, MODULE, "dedup_no_downloads", "No successful downloads to deduplicate",
                    event_id=event_id)
        return {"videos_to_upload": [], "videos_to_replace": [], "videos_to_bump_popularity": [], "skipped_urls": []}
    
    log.info(activity.logger, MODULE, "dedup_started", "Starting deduplication",
             event_id=event_id, successful=len(successful), existing_s3=len(existing_s3_videos) if existing_s3_videos else 0)
    
    # =========================================================================
    # PHASE 1: BATCH DEDUP - Group into duplicate clusters, keep longest+largest
    # =========================================================================
    
    # Build duplicate clusters using union-find approach
    clusters = []  # List of lists, each inner list is a cluster of duplicates
    videos_without_hash = 0  # Track videos that bypass dedup
    comparisons_done = 0
    
    for file_idx, file_info in enumerate(successful):
        perceptual_hash = file_info["perceptual_hash"]
        
        # Heartbeat every video to signal progress (comparison can be slow)
        activity.heartbeat(f"batch_dedup:{file_idx+1}/{len(successful)}")
        
        # Track videos without valid hash - they can't be deduplicated!
        if not perceptual_hash or perceptual_hash == "dense:0.25:":
            videos_without_hash += 1
        
        # Find which cluster(s) this video matches
        matching_cluster_idx = None
        for idx, cluster in enumerate(clusters):
            for member in cluster:
                comparisons_done += 1
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
        log.warning(activity.logger, MODULE, "dedup_missing_hash", "Videos without hash cannot be deduplicated",
                    event_id=event_id, count=videos_without_hash, total=len(successful))
    
    log.info(activity.logger, MODULE, "dedup_clustered", "Clustered videos",
             event_id=event_id, clusters=len(clusters), from_downloads=len(successful))
    
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
        
        # Accumulate popularity from ALL videos in cluster (including their MD5 dedup popularity)
        # This preserves popularity from earlier dedup phases
        total_popularity = sum(v.get("popularity", 1) for v in cluster)
        
        # Pick single best video using smart duration comparison:
        # - If durations differ by >15%, prefer longer video (more complete clip)
        # - If durations are within 15%, prefer higher resolution (larger file)
        # 
        # Why percentage? 10s vs 15s is 50% different (different clips!), but
        # 60s vs 65s is only 8% different (same clip, one slightly trimmed)
        winner = _pick_best_video_from_cluster(cluster)
        winner["popularity"] = total_popularity
        batch_winners.append(winner)
        
        # Find longest and largest for logging
        longest = max(cluster, key=lambda x: x.get("duration", 0))
        largest = max(cluster, key=lambda x: x.get("file_size", 0))
        
        # Log selection with rationale
        max_duration = longest.get("duration", 0) or 1
        durations_similar = all(
            (max_duration - v.get("duration", 0)) / max_duration <= DURATION_SIMILARITY_THRESHOLD
            for v in cluster
        )
        
        if durations_similar:
            log.info(activity.logger, MODULE, "cluster_winner_resolution", "Cluster winner selected",
                     event_id=event_id, duration=round(winner.get('duration', 0), 1),
                     file_size=winner.get('file_size', 0), selection="resolution",
                     cluster_size=len(cluster), popularity=total_popularity)
        else:
            log.info(activity.logger, MODULE, "cluster_winner_length", "Cluster winner selected",
                     event_id=event_id, duration=round(winner.get('duration', 0), 1),
                     file_size=winner.get('file_size', 0), selection="length",
                     cluster_size=len(cluster), popularity=total_popularity)
        
        # Remove all other files in cluster
        for member in cluster:
            if member["file_path"] != winner["file_path"]:
                files_to_remove.append(member["file_path"])
    
    # Clean up discarded files
    cleanup_failures = 0
    for file_path in files_to_remove:
        try:
            os.remove(file_path)
        except Exception as e:
            cleanup_failures += 1
            log.debug(activity.logger, MODULE, "cleanup_file_failed", "Failed to remove discarded file",
                      event_id=event_id, file_path=file_path, error=str(e)[:50])
    
    if cleanup_failures > 0:
        log.warning(activity.logger, MODULE, "cleanup_failures", f"{cleanup_failures} cleanup deletions failed",
                    event_id=event_id, failed=cleanup_failures, total=len(files_to_remove))
    log.info(activity.logger, MODULE, "batch_dedup_complete", "Batch dedup complete",
             event_id=event_id, input=len(successful), keepers=len(batch_winners), removed=len(files_to_remove))
    
    # =========================================================================
    # PHASE 2: S3 DEDUP - Compare batch winners against existing S3 videos for THIS event
    # =========================================================================
    existing_videos_list = existing_s3_videos or []
    if existing_videos_list:
        log.info(activity.logger, MODULE, "s3_comparison_started", "Comparing against S3 videos",
                 event_id=event_id, batch_winners=len(batch_winners), s3_videos=len(existing_videos_list))
    
    videos_to_upload = []  # New videos (no S3 match)
    videos_to_replace = []  # Higher quality than existing S3 video
    videos_to_bump_popularity = []  # Existing S3 is better
    skipped_urls = []
    
    for winner_idx, file_info in enumerate(batch_winners):
        # Heartbeat every video to signal progress during S3 comparison
        activity.heartbeat(f"s3_dedup:{winner_idx+1}/{len(batch_winners)}")
        
        perceptual_hash = file_info["perceptual_hash"]
        file_path = file_info["file_path"]
        file_size = file_info["file_size"]
        duration = file_info.get("duration", 0)
        source_url = file_info.get("source_url", "")
        incoming_popularity = file_info.get("popularity", 1)  # From local dedup
        
        # CRITICAL: Warn if video has no hash - dedup will NOT work!
        if not perceptual_hash or perceptual_hash == "dense:0.25:":
            log.warning(activity.logger, MODULE, "dedup_bypassed", "Video has no perceptual hash - deduplication bypassed",
                        event_id=event_id, source_url=source_url)
        
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
            
            # Use smart comparison: percentage-based duration similarity
            should_replace, reason = _should_replace_s3_video(
                new_duration, new_file_size, existing_duration, existing_file_size
            )
            
            if should_replace:
                new_popularity = existing_popularity + incoming_popularity
                log.info(activity.logger, MODULE, "s3_replace", f"S3 replacement - new is {reason}",
                         event_id=event_id, existing_popularity=existing_popularity, added_popularity=incoming_popularity,
                         total_popularity=new_popularity)
                file_info["popularity"] = new_popularity
                # Pass old S3 key so upload can reuse it (keeps shared URLs stable)
                file_info["_old_s3_key"] = matched_existing.get("_s3_key", "")
                videos_to_replace.append({
                    "new_video": file_info,
                    "old_s3_video": matched_existing
                })
            else:
                # Existing S3 is better - skip but bump popularity
                new_popularity = existing_popularity + incoming_popularity
                log.info(activity.logger, MODULE, "s3_skip", "Existing S3 is better",
                         event_id=event_id, existing_duration=round(existing_duration, 1),
                         existing_size=existing_file_size, old_popularity=existing_popularity,
                         new_popularity=new_popularity)
                videos_to_bump_popularity.append({
                    "s3_video": matched_existing,
                    "new_popularity": new_popularity
                })
                os.remove(file_path)
                if source_url:
                    skipped_urls.append(source_url)
        else:
            # No S3 match - this is a new video
            log.info(activity.logger, MODULE, "new_video", "No S3 match - new video",
                     event_id=event_id, file_size=file_size, duration=round(duration, 1))
            videos_to_upload.append(file_info)
    
    # Log summary
    log.info(activity.logger, MODULE, "dedup_complete", "Deduplication complete",
             event_id=event_id, new_uploads=len(videos_to_upload), s3_replacements=len(videos_to_replace),
             popularity_bumps=len(videos_to_bump_popularity), skipped=len(skipped_urls))
    
    return {
        "videos_to_upload": videos_to_upload,
        "videos_to_replace": videos_to_replace,
        "videos_to_bump_popularity": videos_to_bump_popularity,
        "skipped_urls": skipped_urls,
    }

def _pick_best_video_from_cluster(cluster: List[Dict[str, Any]]) -> Dict[str, Any]:
    """
    Pick the best video from a cluster of perceptual duplicates.
    
    Uses percentage-based duration comparison:
    - If videos are within 15% duration of each other → prefer larger file (higher resolution)
    - If videos differ by >15% → prefer longer duration (more complete clip)
    
    Why percentage? Because:
    - 10s vs 15s = 50% difference → clearly different clips, want longer
    - 60s vs 65s = 8% difference → same clip, slightly trimmed, want better quality
    
    Args:
        cluster: List of video dicts with 'duration' and 'file_size' keys
        
    Returns:
        The best video from the cluster
    """
    if len(cluster) == 1:
        return cluster[0]
    
    # Find the longest video as reference point
    longest = max(cluster, key=lambda x: x.get("duration", 0))
    max_duration = longest.get("duration", 0) or 1  # Avoid division by zero
    
    # Check if all videos are "similar duration" (within threshold of longest)
    all_similar_duration = True
    for video in cluster:
        video_duration = video.get("duration", 0)
        if max_duration > 0:
            # Calculate how much shorter this video is as a percentage
            duration_diff = (max_duration - video_duration) / max_duration
            if duration_diff > DURATION_SIMILARITY_THRESHOLD:
                all_similar_duration = False
                break
    
    if all_similar_duration:
        # All videos are similar length - prefer higher resolution (larger file)
        return max(cluster, key=lambda x: x.get("file_size", 0))
    else:
        # Videos have significantly different lengths - prefer longest
        # Use file_size as tiebreaker for same-duration videos
        return max(cluster, key=lambda x: (x.get("duration", 0), x.get("file_size", 0)))

def _should_replace_s3_video(
    new_duration: float, 
    new_file_size: int, 
    existing_duration: float, 
    existing_file_size: int
) -> tuple[bool, str]:
    """
    Decide whether a new video should replace an existing S3 video.
    
    Uses percentage-based duration comparison:
    - If durations are within 15% → replace only if larger (better quality)
    - If new is significantly longer → replace (more complete)
    - If existing is significantly longer → keep existing
    
    Args:
        new_duration: Duration of new video in seconds
        new_file_size: File size of new video in bytes
        existing_duration: Duration of existing S3 video in seconds
        existing_file_size: File size of existing S3 video in bytes
        
    Returns:
        Tuple of (should_replace: bool, reason: str)
    """
    # Handle edge cases
    if new_duration <= 0 and existing_duration <= 0:
        # No duration info - fall back to file size
        if new_file_size > existing_file_size:
            return True, f"larger ({new_file_size:,} > {existing_file_size:,} bytes)"
        return False, ""
    
    max_duration = max(new_duration, existing_duration)
    if max_duration <= 0:
        max_duration = 1  # Avoid division by zero
    
    # Calculate percentage difference relative to longer video
    duration_diff_pct = abs(new_duration - existing_duration) / max_duration
    
    if duration_diff_pct <= DURATION_SIMILARITY_THRESHOLD:
        # Similar duration - compare by file size (resolution/quality)
        if new_file_size > existing_file_size:
            return True, (
                f"higher quality ({new_file_size:,} > {existing_file_size:,} bytes, "
                f"similar duration {new_duration:.1f}s ≈ {existing_duration:.1f}s)"
            )
        return False, ""
    else:
        # Significantly different duration - prefer longer
        if new_duration > existing_duration:
            return True, f"longer ({new_duration:.1f}s > {existing_duration:.1f}s)"
        return False, ""
