"""
Upload core activities — S3 upload + Mongo writes + temp cleanup.

Companion modules:
  - upload/dedup.py  — MD5 and perceptual dedup activities
  - src/utils/dedup_match.py — pure matching helpers used by upload/dedup.py

See upload/__init__.py for the back-compat re-export surface.
"""

from temporalio import activity
from typing import Any, Dict, List, Optional
import os

from src.data.models import EventFields
from src.utils.errors import S3UnavailableError, S3UploadError
from src.utils.footy_logging import log
from src.utils.config import (
    HASH_VERSION,
    MAX_HAMMING_DISTANCE,
    MIN_CONSECUTIVE_MATCHES,
)

MODULE = "upload"


@activity.defn
async def queue_videos_for_upload(
    fixture_id: int,
    event_id: str,
    player_name: str,
    team_name: str,
    videos: List[dict],
    temp_dir: str,
    failures_by_class: Optional[Dict[str, int]] = None,
) -> Dict[str, Any]:
    """
    Queue videos for upload by signaling the UploadWorkflow.

    Uses Temporal client's signal-with-start to either:
    - Start a new UploadWorkflow if none exists for this event
    - Signal the existing UploadWorkflow to add videos to its queue

    Temporal guarantees signal ordering, so videos are processed FIFO.

    Also records Phase 1 telemetry: videos_validated += len(videos),
    and adds to download_failures_by_class for each typed error class
    the DLWF observed during this attempt.

    Args:
        fixture_id: Fixture ID
        event_id: Event ID
        player_name: Player name for metadata
        team_name: Team name for metadata
        videos: List of video dicts ready for upload
        temp_dir: Temp directory containing video files
        failures_by_class: optional {error_class: count} from this DLWF batch.
            Caller is DownloadWorkflow which accumulates classified failures
            from its per-video exception handler.

    Returns:
        Dict with status and workflow info
    """
    from datetime import timedelta
    from temporalio.common import WorkflowIDReusePolicy
    from src.data.mongo_store import get_store
    from src.utils.temporal_client import get_client
    from src.workflows.upload_workflow import UploadWorkflow, UploadWorkflowInput

    log.info(activity.logger, MODULE, "queue_started", "Queuing videos for upload",
             event_id=event_id, video_count=len(videos),
             failure_classes=list(failures_by_class.keys()) if failures_by_class else [])

    # Telemetry: record validated count + per-class failures for this batch.
    # Done before the actual signal-with-start so even if that errors, we
    # still capture the DLWF's outcome shape.
    store = get_store()
    increments: Dict[str, int] = {}
    if videos:
        increments["videos_validated"] = len(videos)
    if failures_by_class:
        for cls_name, n in failures_by_class.items():
            if n:
                increments[f"download_failures_by_class.{cls_name}"] = n
    if increments:
        store.increment_event_telemetry(
            fixture_id, event_id, increments=increments,
        )

    # If no videos to upload, telemetry is the only side effect — skip the
    # signal-with-start (no point waking UploadWorkflow for an empty batch).
    # DLWF's outer finally still runs the completion check directly.
    if not videos:
        log.info(activity.logger, MODULE, "queue_skipped_empty",
                 "No videos in batch; telemetry recorded, signal skipped",
                 event_id=event_id,
                 failures_recorded=sum(failures_by_class.values()) if failures_by_class else 0)
        return {
            "status": "telemetry_only",
            "workflow_id": None,
            "videos_queued": 0,
        }

    try:
        # Reuse the process-wide Temporal client instead of opening a fresh
        # gRPC connection on every activity invocation (~100+ extra connects
        # per CL night otherwise).
        client = await get_client()

        upload_workflow_id = f"upload-{event_id}"

        # signal-with-start (start_workflow + start_signal): starts the workflow
        # if no instance with this ID is currently RUNNING; otherwise delivers
        # the signal to the existing instance.
        #
        # id_reuse_policy=ALLOW_DUPLICATE means: if a previous UploadWorkflow
        # with this ID already COMPLETED (e.g., idle-timed out after 5min and
        # a late DLWF batch is now arriving), we're allowed to start a fresh
        # instance. Without this, the start would silently fail and the signal
        # would be dropped — the exact failure mode that caused the Lazio Pisa
        # stuck-event symptom (audit §1i). With DLWF now owning completion
        # marking, late-batch arrivals after a Completed UploadWorkflow are
        # rare, but this keeps the fallback path correct.
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
            id_reuse_policy=WorkflowIDReusePolicy.ALLOW_DUPLICATE,
            # Increase task timeout from 10s to 60s - large histories need more
            # time to replay, otherwise we get "Task not found" errors
            task_timeout=timedelta(seconds=60),
        )

        log.info(activity.logger, MODULE, "queue_success", "Queued videos successfully",
                 event_id=event_id, workflow_id=upload_workflow_id)

        return {
            "status": "queued",
            "workflow_id": upload_workflow_id,
            "videos_queued": len(videos),
        }

    except Exception as e:
        log.error(activity.logger, MODULE, "queue_failed", "Failed to queue videos",
                  event_id=event_id, error=str(e))
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
    from src.data.mongo_store import FootyMongoStore, get_store
    
    store = get_store()
    
    log.info(activity.logger, MODULE, "fetch_event_data", "Fetching event data",
             fixture_id=fixture_id, event_id=event_id)
    
    # Get fixture
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        log.error(activity.logger, MODULE, "fixture_not_found", "Fixture not found",
                  fixture_id=fixture_id, event_id=event_id)
        return {"status": "error", "error": "fixture_not_found"}
    
    # Find event
    event = None
    for evt in fixture.get("events", []):
        evt_id = evt.get(EventFields.EVENT_ID)
        if evt_id == event_id:
            event = evt
            break
    
    if not event:
        log.error(activity.logger, MODULE, "event_not_found", "Event not found",
                  fixture_id=fixture_id, event_id=event_id)
        return {"status": "error", "error": "event_not_found"}
    
    discovered_videos = event.get(EventFields.DISCOVERED_VIDEOS, [])
    if not discovered_videos:
        log.warning(activity.logger, MODULE, "no_discovered_videos", "No discovered videos",
                    event_id=event_id)
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
            # Timestamp verification fields (Phase 1)
            "timestamp_verified": video_obj.get("timestamp_verified", False),
            "extracted_minute": video_obj.get("extracted_minute"),
            "timestamp_status": video_obj.get("timestamp_status", "unverified"),
        }
        existing_s3_videos.append(video_info)
        log.debug(activity.logger, MODULE, "existing_video", "Found existing video",
                  event_id=event_id, s3_key=s3_key, width=video_info['width'], height=video_info['height'],
                  popularity=video_info['popularity'])
    
    # Filter discovered_videos to only NEW ones (URLs not already downloaded for this event)
    videos_to_download = []
    skipped_already_downloaded = 0
    
    for video in discovered_videos:
        video_url = video.get("tweet_url") or video.get("video_page_url")
        if video_url in already_downloaded_urls:
            skipped_already_downloaded += 1
            log.debug(activity.logger, MODULE, "skip_already_downloaded", "Skipping already downloaded",
                      event_id=event_id, url=video_url[:50])
        else:
            videos_to_download.append(video)
    
    if not videos_to_download:
        log.info(activity.logger, MODULE, "no_new_videos", "No new videos to download",
                 event_id=event_id, already_in_s3=skipped_already_downloaded)
        return {
            "status": "no_videos",
            "discovered_videos": [],
            "event": event,
            "existing_s3_videos": existing_s3_videos,
        }
    
    if existing_s3_videos:
        log.info(activity.logger, MODULE, "existing_for_dedup", "Existing S3 videos for dedup",
                 event_id=event_id, count=len(existing_s3_videos))
    
    log.info(activity.logger, MODULE, "videos_found", "Found videos to process",
             event_id=event_id, to_download=len(videos_to_download),
             skipped_already_in_s3=skipped_already_downloaded)
    
    return {
        "status": "success",
        "discovered_videos": videos_to_download,
        "player_name": player_name,
        "team_name": team_name,
        "event": event,
        "existing_s3_videos": existing_s3_videos,  # Full metadata for quality comparison
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
    timestamp_verified: bool = False,  # True if clock matched API time
    extracted_minute: int = None,       # Best extracted clock minute
    timestamp_status: str = "unverified",  # "verified" / "unverified" / "rejected" (rejected never stored)
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
    from src.data.s3_store import FootyS3Store, get_s3_store
    
    # For replacements, reuse the existing S3 key to keep URLs stable
    # This allows shared links to remain valid when video quality is upgraded
    if existing_s3_key:
        s3_key = existing_s3_key
        log.info(activity.logger, MODULE, "reusing_s3_key", "Reusing S3 key for replacement",
                 event_id=event_id, s3_key=s3_key)
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
    log.info(activity.logger, MODULE, "s3_upload_started", "Starting S3 upload",
             event_id=event_id, video_idx=video_index, quality=quality_info,
             popularity=popularity, s3_key=s3_key)
    
    s3_store = get_s3_store()
    try:
        s3_url = s3_store.upload_video(file_path, s3_key, metadata=metadata)
    except Exception as e:
        # Classify based on the underlying error shape. boto/botocore's ClientError
        # surfaces NoSuchBucket / EndpointConnectionError / etc. as substrings.
        err_str = str(e)
        ctx = {
            "event_id": event_id,
            "video_idx": video_index,
            "s3_key": s3_key,
            "file_size": file_size,
            "error_detail": err_str[:200],
        }
        if "EndpointConnectionError" in err_str or "Could not connect" in err_str:
            err = S3UnavailableError("MinIO endpoint unreachable", context=ctx)
        else:
            err = S3UploadError(f"S3 upload failed: {err_str[:120]}", context=ctx)
        log.error(activity.logger, MODULE, "s3_upload_failed",
                  "S3 upload raised", **err.log_fields())
        raise err from e

    if not s3_url:
        err = S3UploadError(
            f"S3 upload returned None for event={event_id} video_idx={video_index}",
            context={"event_id": event_id, "video_idx": video_index, "s3_key": s3_key},
        )
        log.error(activity.logger, MODULE, "s3_upload_failed", "S3 returned None",
                  **err.log_fields())
        raise err

    # Telemetry: count this S3 upload + seed first_s3_upload_at on the
    # first write per event. Replacements (reuse of existing_s3_key) still
    # represent successful S3 PUTs so they also count here.
    from datetime import datetime, timezone
    from src.data.mongo_store import get_store
    _store = get_store()
    _now = datetime.now(timezone.utc)
    _store.increment_event_telemetry(
        fixture_id, event_id,
        increments={"videos_uploaded_to_s3": 1},
        min_fields={"first_s3_upload_at": _now},
    )

    log.info(activity.logger, MODULE, "s3_upload_success", "Uploaded to S3",
             event_id=event_id, video_idx=video_index, s3_url=s3_url)
    
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
            # Timestamp verification (Phase 1)
            "timestamp_verified": timestamp_verified,
            "extracted_minute": extracted_minute,
            "timestamp_status": timestamp_status,
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
    from src.data.mongo_store import FootyMongoStore, get_store
    
    store = get_store()
    
    log.info(activity.logger, MODULE, "replace_started", "Atomic in-place update",
             event_id=event_id, url=s3_url.split('/')[-1])
    
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
                                log.info(activity.logger, MODULE, "replace_complete", "In-place update complete",
                                         event_id=event_id, collection=collection_name)
                                return True
                            else:
                                log.warning(activity.logger, MODULE, "replace_no_modification", "No modification made",
                                            event_id=event_id)
                                return False
                    break
            
        except Exception as e:
            log.error(activity.logger, MODULE, "replace_failed", "In-place update failed",
                      event_id=event_id, collection=collection_name, error=str(e))
    
    log.warning(activity.logger, MODULE, "replace_not_found", "Video not found for in-place update",
                 event_id=event_id, url=s3_url)
    return False

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
    from src.data.mongo_store import FootyMongoStore, get_store
    
    store = get_store()
    
    log.info(activity.logger, MODULE, "popularity_bump_started", "Bumping video popularity",
             event_id=event_id, new_popularity=new_popularity)
    
    success = store.update_video_popularity(fixture_id, event_id, s3_url, new_popularity)
    
    if success:
        log.info(activity.logger, MODULE, "popularity_bump_success", "Popularity updated",
                 event_id=event_id, popularity=new_popularity)
    else:
        log.warning(activity.logger, MODULE, "popularity_bump_failed", "Popularity update failed",
                    event_id=event_id, url=s3_url)
    
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
    from src.data.mongo_store import FootyMongoStore, get_store

    store = get_store()
    
    log.info(activity.logger, MODULE, "save_videos_started", "Saving video objects",
             event_id=event_id, count=len(video_objects))
    
    success = store.add_videos_to_event(fixture_id, event_id, video_objects)
    
    if not success:
        log.warning(activity.logger, MODULE, "save_videos_failed", "Failed to save video objects",
                    event_id=event_id)
    
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
    from src.data.mongo_store import FootyMongoStore, get_store

    store = get_store()
    
    log.info(activity.logger, MODULE, "recalc_ranks", "Recalculating video ranks",
             event_id=event_id)
    
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
                log.warning(activity.logger, MODULE, "file_delete_failed", "Failed to delete file",
                            file_path=file_path, error=str(e))
    
    if deleted > 0:
        log.info(activity.logger, MODULE, "files_deleted", "Deleted individual files",
                 count=deleted)
    
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
                log.info(activity.logger, MODULE, "temp_dir_deleted", "Deleted temp dir",
                         path=temp_dir)
            except Exception as e:
                log.warning(activity.logger, MODULE, "temp_dir_delete_failed", "Failed to delete temp dir",
                            path=temp_dir, error=str(e))
    
    log.info(activity.logger, MODULE, "fixture_cleanup_complete", "Fixture cleanup complete",
             fixture_id=fixture_id, dirs_deleted=deleted)
    
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
        log.info(activity.logger, MODULE, "upload_temp_cleaned", "Cleaned up temp dir",
                 path=temp_dir)
        return True
    
    return False
