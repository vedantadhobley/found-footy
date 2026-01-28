"""Upload activities - S3 deduplication and upload operations for UploadWorkflow"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os

from src.data.models import EventFields
from src.utils.config import (
    HASH_VERSION,
    MAX_HAMMING_DISTANCE,
    MIN_CONSECUTIVE_MATCHES,
)


@activity.defn
async def queue_videos_for_upload(
    fixture_id: int,
    event_id: str,
    player_name: str,
    team_name: str,
    videos: List[dict],
    temp_dir: str,
) -> Dict[str, Any]:
    """
    Queue videos for upload by signaling the UploadWorkflow.
    
    Uses Temporal client's signal-with-start to either:
    - Start a new UploadWorkflow if none exists for this event
    - Signal the existing UploadWorkflow to add videos to its queue
    
    Temporal guarantees signal ordering, so videos are processed FIFO.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        player_name: Player name for metadata
        team_name: Team name for metadata
        videos: List of video dicts ready for upload
        temp_dir: Temp directory containing video files
    
    Returns:
        Dict with status and workflow info
    """
    from temporalio.client import Client
    from src.workflows.upload_workflow import UploadWorkflow, UploadWorkflowInput
    
    temporal_host = os.getenv("TEMPORAL_HOST", "localhost:7233")
    
    activity.logger.info(
        f"📨 [UPLOAD] Queuing {len(videos)} videos for upload | event={event_id}"
    )
    
    try:
        # Connect to Temporal
        client = await Client.connect(temporal_host)
        
        upload_workflow_id = f"upload-{event_id}"
        
        # Use signal-with-start: starts workflow if not exists, signals if exists
        # The "add_videos" signal will be delivered to add videos to the queue
        from datetime import timedelta
        await client.start_workflow(
            UploadWorkflow.run,
            UploadWorkflowInput(
                fixture_id=fixture_id,
                event_id=event_id,
                player_name=player_name,
                team_name=team_name,
                videos=[],  # Initial input empty - videos come via signal
                temp_dir=temp_dir,
            ),
            id=upload_workflow_id,
            task_queue="found-footy",
            start_signal="add_videos",
            start_signal_args=[{
                "player_name": player_name,
                "team_name": team_name,
                "videos": videos,
                "temp_dir": temp_dir,
            }],
            # Increase task timeout from 10s to 60s - large histories need more
            # time to replay, otherwise we get "Task not found" errors
            task_timeout=timedelta(seconds=60),
        )
        
        activity.logger.info(
            f"✅ [UPLOAD] Queued videos successfully | workflow={upload_workflow_id} | event={event_id}"
        )
        
        return {
            "status": "queued",
            "workflow_id": upload_workflow_id,
            "videos_queued": len(videos),
        }
        
    except Exception as e:
        activity.logger.error(
            f"❌ [UPLOAD] Failed to queue videos | error={e} | event={event_id}"
        )
        return {
            "status": "error",
            "error": str(e),
        }


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
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    activity.logger.info(
        f"📥 [UPLOAD] fetch_event_data | fixture={fixture_id} | event={event_id}"
    )
    
    # Get fixture
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        activity.logger.error(
            f"❌ [UPLOAD] Fixture not found | fixture={fixture_id}"
        )
        return {"status": "error", "error": "fixture_not_found"}
    
    # Find event
    event = None
    for evt in fixture.get("events", []):
        evt_id = evt.get(EventFields.EVENT_ID)
        if evt_id == event_id:
            event = evt
            break
    
    if not event:
        activity.logger.error(
            f"❌ [UPLOAD] Event not found | fixture={fixture_id} | event={event_id}"
        )
        return {"status": "error", "error": "event_not_found"}
    
    discovered_videos = event.get(EventFields.DISCOVERED_VIDEOS, [])
    if not discovered_videos:
        activity.logger.warning(
            f"⚠️ [UPLOAD] No discovered videos | event={event_id}"
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
        # NOTE: Use "_s3_key" to match what deduplicate_videos expects
        video_info = {
            "s3_url": s3_url,
            "_s3_key": s3_key,  # Underscore prefix for internal use, matches dedup code
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
    
    # Filter discovered_videos to only NEW ones (URLs not already downloaded for this event)
    videos_to_download = []
    skipped_already_downloaded = 0
    
    for video in discovered_videos:
        video_url = video.get("tweet_url") or video.get("video_page_url")
        if video_url in already_downloaded_urls:
            skipped_already_downloaded += 1
            activity.logger.debug(
                f"⏭️ [UPLOAD] Skipping already downloaded | url={video_url[:50]}..."
            )
        else:
            videos_to_download.append(video)
    
    if not videos_to_download:
        activity.logger.info(
            f"⏭️ [UPLOAD] No new videos | event={event_id} | "
            f"already_in_s3={skipped_already_downloaded}"
        )
        return {
            "status": "no_videos",
            "discovered_videos": [],
            "event": event,
            "existing_s3_videos": existing_s3_videos,
        }
    
    if existing_s3_videos:
        activity.logger.info(
            f"🔍 [UPLOAD] Existing S3 videos for dedup | count={len(existing_s3_videos)}"
        )
    
    activity.logger.info(
        f"📥 [UPLOAD] Found videos | event={event_id} | "
        f"to_download={len(videos_to_download)} | skipped_already_in_s3={skipped_already_downloaded}"
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
        - md5_duplicates_removed: Count of removed duplicates
        - s3_exact_matches: Videos matching S3 by MD5 (for popularity bump)
        - s3_replacements: Videos that should replace S3 (same MD5, better quality)
    """
    # Filter to successful downloads only
    successful = [f for f in downloaded_files if f.get("status") == "success"]
    
    if not successful:
        activity.logger.info(f"📤 [MD5-DEDUP] No successful downloads to deduplicate")
        return {
            "unique_videos": [],
            "md5_duplicates_removed": 0,
            "s3_exact_matches": [],
            "s3_replacements": [],
        }
    
    activity.logger.info(
        f"🔍 [MD5-DEDUP] Starting | downloads={len(successful)} | "
        f"existing_s3={len(existing_s3_videos) if existing_s3_videos else 0}"
    )
    
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
    }


@activity.defn
async def deduplicate_videos(
    downloaded_files: List[Dict[str, Any]],
    existing_s3_videos: Optional[List[Dict[str, Any]]] = None
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
            activity.logger.info(
                f"🏆 Cluster winner: {winner.get('duration', 0):.1f}s, {winner.get('file_size', 0):,} bytes "
                f"(preferred RESOLUTION - similar durations, {len(cluster)} dups, pop={total_popularity})"
            )
        else:
            activity.logger.info(
                f"🏆 Cluster winner: {winner.get('duration', 0):.1f}s, {winner.get('file_size', 0):,} bytes "
                f"(preferred LENGTH - varying durations, {len(cluster)} dups, pop={total_popularity})"
            )
        
        # Remove all other files in cluster
        for member in cluster:
            if member["file_path"] != winner["file_path"]:
                files_to_remove.append(member["file_path"])
    
    # Clean up discarded files
    for file_path in files_to_remove:
        try:
            os.remove(file_path)
        except Exception:
            pass
    
    activity.logger.info(f"📊 Batch dedup: {len(successful)} → {len(batch_winners)} keepers ({len(files_to_remove)} removed)")
    
    # =========================================================================
    # PHASE 2: S3 DEDUP - Compare batch winners against existing S3 videos for THIS event
    # =========================================================================
    existing_videos_list = existing_s3_videos or []
    if existing_videos_list:
        activity.logger.info(f"📦 Comparing {len(batch_winners)} videos against {len(existing_videos_list)} S3 videos...")
    
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
            
            # Use smart comparison: percentage-based duration similarity
            should_replace, reason = _should_replace_s3_video(
                new_duration, new_file_size, existing_duration, existing_file_size
            )
            
            if should_replace:
                new_popularity = existing_popularity + incoming_popularity
                activity.logger.info(
                    f"🔄 S3 REPLACE: New is {reason} "
                    f"- popularity {existing_popularity} + {incoming_popularity} = {new_popularity}"
                )
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
        "skipped_urls": skipped_urls,
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
    existing_s3_key: str = "",  # For replacements: reuse old S3 key to keep URL stable
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
        existing_s3_key: For replacements - reuse old S3 key so shared URLs stay valid
    
    Returns:
        Dict with s3_url, perceptual_hash, and status
    
    Raises:
        Exception: If S3 upload fails (for Temporal retry)
    """
    from src.data.s3_store import FootyS3Store
    
    # For replacements, reuse the existing S3 key to keep URLs stable
    # This allows shared links to remain valid when video quality is upgraded
    if existing_s3_key:
        s3_key = existing_s3_key
        activity.logger.info(
            f"♻️ [UPLOAD] Reusing S3 key for replacement | key={s3_key}"
        )
    elif file_hash:
        filename = f"{event_id}_{file_hash[:8]}.mp4"
        s3_key = f"{fixture_id}/{event_id}/{filename}"
    else:
        # Fallback without hash - use timestamp for uniqueness
        import time
        filename = f"{event_id}_{int(time.time())}.mp4"
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
async def update_video_in_place(
    fixture_id: int,
    event_id: str,
    s3_url: str,
    new_video_object: Dict[str, Any],
) -> bool:
    """
    Atomically update an existing video entry in MongoDB.
    Used for in-place replacements where the S3 URL stays the same.
    
    This avoids the race condition of add+remove where the video temporarily disappears.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        s3_url: The S3 URL (stays the same, used to find the entry)
        new_video_object: New video metadata to replace the old entry
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    activity.logger.info(
        f"♻️ [REPLACE] Atomic in-place update | event={event_id} | url={s3_url.split('/')[-1]}"
    )
    
    # Try both active and completed collections
    for collection_name, collection in [
        ("active", store.fixtures_active),
        ("completed", store.fixtures_completed)
    ]:
        try:
            # First find the event and get the video array index
            fixture = collection.find_one(
                {"_id": fixture_id, f"events.{EventFields.EVENT_ID}": event_id}
            )
            if not fixture:
                continue
            
            # Find event index and video index
            for event_idx, evt in enumerate(fixture.get("events", [])):
                if evt.get(EventFields.EVENT_ID) == event_id:
                    videos = evt.get(EventFields.S3_VIDEOS, [])
                    for video_idx, video in enumerate(videos):
                        if video.get("url") == s3_url:
                            # Found it! Update in place using positional operator
                            update_path = f"events.{event_idx}.{EventFields.S3_VIDEOS}.{video_idx}"
                            result = collection.update_one(
                                {"_id": fixture_id},
                                {"$set": {update_path: new_video_object}}
                            )
                            if result.modified_count > 0:
                                activity.logger.info(
                                    f"✅ [REPLACE] In-place update complete | event={event_id} | "
                                    f"collection={collection_name}"
                                )
                                return True
                            else:
                                activity.logger.warning(
                                    f"⚠️ [REPLACE] No modification made | event={event_id}"
                                )
                                return False
                    break
            
        except Exception as e:
            activity.logger.error(
                f"❌ [REPLACE] In-place update failed | collection={collection_name} | error={e}"
            )
    
    activity.logger.warning(
        f"⚠️ [REPLACE] Video not found for in-place update | event={event_id} | url={s3_url}"
    )
    return False


@activity.defn
async def replace_s3_video(
    fixture_id: int,
    event_id: str,
    old_s3_url: str,
    old_s3_key: str,
    skip_s3_delete: bool = False,  # True when overwriting with same key (keeps URLs stable)
) -> bool:
    """
    Remove old video entry from MongoDB, optionally delete from S3.
    Called when a higher quality version is being uploaded to replace it.
    
    NOTE: For same-URL replacements (where S3 key is reused), use update_video_in_place
    instead to avoid the race condition where video temporarily disappears.
    
    When skip_s3_delete=True, the S3 file is NOT deleted because the new upload
    will overwrite the same key. This keeps shared URLs stable.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        old_s3_url: Full S3 URL to remove from MongoDB
        old_s3_key: S3 key for deletion
        skip_s3_delete: If True, skip S3 deletion (file will be overwritten)
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.s3_store import FootyS3Store
    
    if skip_s3_delete:
        activity.logger.info(
            f"♻️ [REPLACE] Skipping S3 delete (will overwrite) | event={event_id} | key={old_s3_key}"
        )
    else:
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


@activity.defn
async def save_video_objects(
    fixture_id: int,
    event_id: str,
    video_objects: List[Dict[str, Any]],
) -> bool:
    """
    Save video objects to MongoDB _s3_videos array.
    Used by UploadWorkflow to save uploaded video objects.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        video_objects: List of video objects {url, perceptual_hash, resolution_score, popularity, rank}
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore

    store = FootyMongoStore()
    
    activity.logger.info(
        f"💾 [UPLOAD] Saving video objects | event={event_id} | count={len(video_objects)}"
    )
    
    success = store.add_videos_to_event(fixture_id, event_id, video_objects)
    
    if not success:
        activity.logger.warning(f"⚠️ [UPLOAD] Failed to save video objects | event={event_id}")
    
    return success


@activity.defn
async def recalculate_video_ranks(fixture_id: int, event_id: str) -> bool:
    """
    Recalculate ranks for all videos in an event.
    Called by UploadWorkflow after uploading new videos.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
    
    Returns:
        True if successful
    """
    from src.data.mongo_store import FootyMongoStore

    store = FootyMongoStore()
    
    activity.logger.info(f"📊 [UPLOAD] Recalculating video ranks | event={event_id}")
    
    return store.recalculate_video_ranks(fixture_id, event_id)


@activity.defn
async def cleanup_individual_files(file_paths: List[str]) -> int:
    """
    Clean up individual files (not entire directories).
    Used to delete files after successful S3 upload.
    
    Args:
        file_paths: List of file paths to delete
    
    Returns:
        Number of files deleted
    """
    deleted = 0
    for file_path in file_paths:
        if file_path and os.path.exists(file_path):
            try:
                os.remove(file_path)
                deleted += 1
            except Exception as e:
                activity.logger.warning(f"⚠️ [UPLOAD] Failed to delete file: {file_path} | error={e}")
    
    if deleted > 0:
        activity.logger.info(f"🧹 [UPLOAD] Deleted {deleted} individual files")
    
    return deleted


@activity.defn
async def cleanup_fixture_temp_dirs(fixture_id: int) -> int:
    """
    Clean up ALL temp directories for a given fixture.
    Called by MonitorWorkflow when fixture moves to completed.
    
    Matches: /tmp/found-footy/{fixture_id}_* (all events for this fixture)
    
    Args:
        fixture_id: Fixture ID (e.g., 1378015)
    
    Returns:
        Number of directories deleted
    """
    import shutil
    import glob
    
    # Match all temp dirs for this fixture (any event)
    # Uses /tmp/found-footy which is mounted as a shared volume across workers
    pattern = f"/tmp/found-footy/{fixture_id}_*"
    matching_dirs = glob.glob(pattern)
    
    deleted = 0
    for temp_dir in matching_dirs:
        if os.path.isdir(temp_dir):
            try:
                shutil.rmtree(temp_dir)
                deleted += 1
                activity.logger.info(f"🧹 [CLEANUP] Deleted temp dir | path={temp_dir}")
            except Exception as e:
                activity.logger.warning(f"⚠️ [CLEANUP] Failed to delete: {temp_dir} | error={e}")
    
    activity.logger.info(f"🧹 [CLEANUP] Fixture cleanup complete | fixture={fixture_id} | dirs_deleted={deleted}")
    
    return deleted


@activity.defn
async def cleanup_upload_temp(temp_dir: str) -> bool:
    """
    Clean up temporary directory.
    
    Args:
        temp_dir: Path to the temporary directory to delete
    
    Returns:
        True if successful
    """
    import shutil
    
    if temp_dir and os.path.exists(temp_dir):
        shutil.rmtree(temp_dir)
        activity.logger.info(f"🧹 [UPLOAD] Cleaned up temp dir | path={temp_dir}")
        return True
    
    return False


# ==============================================================================
# Helper functions for perceptual hash comparison
# ==============================================================================

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


# Threshold for "same duration" - if videos differ by less than this percentage,
# they're considered the same length and we prefer higher resolution (file size)
DURATION_SIMILARITY_THRESHOLD = 0.15  # 15%


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
