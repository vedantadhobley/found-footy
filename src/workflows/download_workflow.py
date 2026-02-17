"""
Download Workflow - Video Download and Validation Pipeline

Orchestrates video download, validation, and hash generation:
1. register_download_workflow - FIRST THING: Register ourselves (proves we're running!)
2. download_single_video x N - Download each video individually (3 retries per video)
3. deduplicate_by_md5 - FAST MD5 dedup within batch (eliminates true duplicates)
4. validate_video_is_soccer x N - AI validation (only for MD5-unique videos)
5. generate_video_hash x N - Generate perceptual hash (only for validated videos)
6. queue_videos_for_upload - ALWAYS signal UploadWorkflow (even with 0 videos)

Key Design: Uses signal-with-start to queue video batches to UploadWorkflow.
- queue_videos_for_upload activity uses Temporal client to signal-with-start
- Temporal guarantees signal delivery order = FIFO processing
- Only ONE UploadWorkflow per event, processes batches sequentially
- Different events can have parallel UploadWorkflows

Workflow-ID-Based Tracking (NEW):
- register_download_workflow is called FIRST (before any other work)
- Uses $addToSet for idempotency (same workflow ID won't double-count)
- If workflow fails to start: doesn't register → count stays low → Twitter retries
- If workflow crashes and restarts: re-registers → no-op (already in array)
- UploadWorkflow checks count and marks _download_complete when 10 reached

Design Philosophy:
- Per-video retry (3 attempts with exponential backoff)
- Videos that fail all 3 attempts are logged but don't block workflow
- Parallel processing where possible (downloads, hashes)
- Comprehensive logging at every step with [DOWNLOAD] prefix
- Activity-level heartbeats for long operations (hash generation)
- ALWAYS signal UploadWorkflow - ensures completion check happens

Pipeline Order:
- MD5 batch dedup happens BEFORE AI validation (saves expensive AI calls)
- AI validation happens BEFORE perceptual hash generation
- This saves expensive hash computation for non-soccer/duplicate videos
- Hash generation uses heartbeats (sends heartbeat every 5 frames)

Started by: TwitterWorkflow (with ABANDON policy - doesn't wait)
Queues to: UploadWorkflow via signal-with-start (FIFO per event)
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
import asyncio

with workflow.unsafe.imports_passed_through():
    from src.activities import download as download_activities
    from src.utils.footy_logging import log

MODULE = "download_workflow"


@workflow.defn
class DownloadWorkflow:
    """
    Download, validate, and hash videos, then delegate to UploadWorkflow.
    
    Uses granular activities for proper retry semantics:
    - Each video download is independent
    - Failures don't cascade (partial success preserved)
    - UploadWorkflow serializes S3 operations per event
    
    Key: Registers itself at START (proves we're running!) before any other work.
    
    Note: VAR check is done by TwitterWorkflow before spawning this workflow.
    """
    
    @workflow.run
    async def run(
        self,
        fixture_id: int,
        event_id: str,
        player_name: str = "",
        team_name: str = "",
        discovered_videos: list = None,
        event_minute: int = 0,
        event_extra: int | None = None,
    ) -> dict:
        """
        Execute the video download pipeline.
        
        Args:
            fixture_id: The fixture ID
            event_id: The event ID
            player_name: Player name for S3 metadata
            team_name: Team name for S3 metadata
            discovered_videos: List of videos from Twitter search (passed directly)
            event_minute: API elapsed minute (e.g., 45, 90) — default 0 for Temporal replay safety
            event_extra: API extra/stoppage minutes (e.g., 3), or None
        
        Returns:
            Dict with videos_uploaded count and s3_urls list
        """
        log.info(workflow.logger, MODULE, "started", "DownloadWorkflow STARTED",
                 event_id=event_id, videos=len(discovered_videos) if discovered_videos else 0)
        
        # =========================================================================
        # Step 0: REGISTER OURSELVES (proves we're running!)
        # This is CRITICAL - it must be the FIRST thing we do.
        # If we crash after this, we're still counted (which is correct).
        # If we never run this, TwitterWorkflow will keep spawning us.
        # =========================================================================
        workflow_id = workflow.info().workflow_id
        log.info(workflow.logger, MODULE, "registering_workflow",
                 "Registering workflow", workflow_id=workflow_id, event_id=event_id)
        
        try:
            register_result = await workflow.execute_activity(
                download_activities.register_download_workflow,
                args=[fixture_id, event_id, workflow_id],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=5,  # Important - retry hard
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            download_count = register_result.get("count", 0)
            log.info(workflow.logger, MODULE, "registered",
                     "Workflow registered", workflow_id=workflow_id,
                     download_count=download_count, event_id=event_id)
        except Exception as e:
            log.error(workflow.logger, MODULE, "register_failed",
                      "FAILED to register workflow - Continuing anyway",
                      event_id=event_id, error=str(e))
        
        # Initialize download stats for visibility into what happened in the pipeline
        download_stats = {
            "discovered": len(discovered_videos) if discovered_videos else 0,
            "downloaded": 0,
            "filtered_aspect_duration": 0,
            "download_failed": 0,
            "md5_batch_deduped": 0,
            "ai_rejected": 0,
            "ai_validation_failed": 0,
            "timestamp_rejected": 0,
            "hash_generated": 0,
            "hash_failed": 0,
            "sent_to_upload": 0,
        }
        
        if not discovered_videos:
            log.info(workflow.logger, MODULE, "no_videos",
                     "No videos to download", event_id=event_id)
            # ALWAYS signal UploadWorkflow - it will check count and mark complete if needed
            await self._signal_upload_workflow(fixture_id, event_id, [], "/tmp/dummy")
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        log.info(workflow.logger, MODULE, "processing",
                 "Processing videos", count=len(discovered_videos), event_id=event_id)
        
        # Temp directory path with unique run ID to prevent conflicts between concurrent workflows
        # Uses /tmp/found-footy which is mounted as a shared volume across all worker replicas
        # This ensures activities on any worker can access files downloaded by other workers
        run_id = workflow.info().run_id[:8]
        temp_dir = f"/tmp/found-footy/{event_id}_{run_id}"
        
        # =========================================================================
        # Step 1: Download videos IN PARALLEL (with per-video retry)
        # 403 errors are common (rate limits, expired links) - retry 3x with backoff
        # =========================================================================
        log.info(workflow.logger, MODULE, "downloading",
                 "Downloading videos in parallel",
                 count=len(discovered_videos), event_id=event_id)
        
        download_results = []
        filtered_urls = []  # Track URLs filtered out (too short/long/vertical)
        failed_urls = []    # Track URLs that failed after 3 retries
        
        # Create download tasks for parallel execution
        async def download_video(idx: int, video: dict):
            video_url = video.get("tweet_url") or video.get("video_page_url")
            if not video_url:
                log.warning(workflow.logger, MODULE, "video_no_url",
                            "Video has no URL, skipping",
                            idx=idx, event_id=event_id)
                return None
            
            try:
                result = await workflow.execute_activity(
                    download_activities.download_single_video,
                    args=[video_url, idx, event_id, temp_dir, video_url],
                    start_to_close_timeout=timedelta(seconds=90),  # Supports up to 90s videos
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                        maximum_interval=timedelta(seconds=10),
                    ),
                )
                return {"idx": idx, "result": result, "url": video_url}
            except Exception as e:
                log.warning(workflow.logger, MODULE, "video_failed",
                            "Video FAILED after 3 retries",
                            idx=idx, url=video_url[:50], error=str(e)[:100],
                            event_id=event_id)
                return {
                    "idx": idx, 
                    "result": {"status": "failed", "error": str(e)[:200], "source_url": video_url}, 
                    "url": video_url, 
                    "failed": True
                }
        
        # Execute all downloads in parallel
        download_tasks = [download_video(idx, video) for idx, video in enumerate(discovered_videos)]
        download_outcomes = await asyncio.gather(*download_tasks)
        
        # Process results
        for outcome in download_outcomes:
            if outcome is None:
                continue
            
            result = outcome["result"]
            
            # Handle multi-video tweets - flatten the results
            if result.get("status") == "multi_video":
                videos = result.get("videos", [])
                log.info(workflow.logger, MODULE, "multi_video_tweet",
                         "Multi-video tweet found",
                         idx=outcome['idx'], count=len(videos), event_id=event_id)
                download_results.extend(videos)
            else:
                download_results.append(result)
            
            # Track filtered videos (too short/long/vertical)
            if result.get("status") == "filtered":
                source_url = result.get("source_url")
                if source_url:
                    filtered_urls.append(source_url)
            
            # Track failed URLs
            if outcome.get("failed"):
                failed_urls.append(outcome["url"])
        
        successful_downloads = sum(1 for r in download_results if r.get("status") == "success")
        download_stats["downloaded"] = successful_downloads
        download_stats["filtered_aspect_duration"] = len(filtered_urls)
        download_stats["download_failed"] = len(failed_urls)
        
        log.info(workflow.logger, MODULE, "downloads_complete",
                 "Downloads complete", success=successful_downloads,
                 filtered=len(filtered_urls), failed=len(failed_urls), event_id=event_id)
        
        # =========================================================================
        # Step 2: MD5 Dedup within batch - Fast elimination of true duplicates
        # This only deduplicates WITHIN this batch (not against S3 - that's UploadWorkflow's job)
        # =========================================================================
        successful_videos = [r for r in download_results if r.get("status") == "success"]
        
        if successful_videos:
            log.info(workflow.logger, MODULE, "md5_dedup_start",
                     "Running fast MD5 batch dedup",
                     videos=len(successful_videos), event_id=event_id)
            
            # Simple batch dedup by MD5 - just remove identical files within this batch
            seen_hashes = {}
            unique_videos = []
            batch_dupes = 0
            
            for video in successful_videos:
                file_hash = video.get("file_hash", "")
                if not file_hash:
                    unique_videos.append(video)
                elif file_hash in seen_hashes:
                    batch_dupes += 1
                    log.debug(workflow.logger, MODULE, "batch_duplicate",
                              "Batch duplicate found",
                              url=video.get('source_url', '')[:50])
                else:
                    seen_hashes[file_hash] = True
                    unique_videos.append(video)
            
            download_stats["md5_batch_deduped"] = batch_dupes
            successful_videos = unique_videos
            
            log.info(workflow.logger, MODULE, "md5_dedup_complete",
                     "MD5 batch dedup complete",
                     unique=len(successful_videos), batch_dupes=batch_dupes, event_id=event_id)
        
        # =========================================================================
        # Step 3: AI Validation (only for MD5-unique videos - saves compute!)
        # Validates videos are soccer content - rejects non-soccer early
        # =========================================================================
        validated_videos = []
        rejected_count = 0
        validation_failed_count = 0
        
        if successful_videos:
            log.info(workflow.logger, MODULE, "ai_validation_start",
                     "Validating videos with AI vision",
                     count=len(successful_videos), event_id=event_id)
        
        for video_info in successful_videos:
            try:
                validation = await workflow.execute_activity(
                    download_activities.validate_video_is_soccer,
                    args=[video_info["file_path"], event_id, event_minute, event_extra],
                    start_to_close_timeout=timedelta(seconds=90),
                    retry_policy=RetryPolicy(
                        maximum_attempts=4,
                        initial_interval=timedelta(seconds=3),
                        backoff_coefficient=2.0,
                        maximum_interval=timedelta(seconds=30),
                    ),
                )
                
                if validation.get("is_valid", True):
                    # Attach verification fields to video_info for downstream
                    video_info["clock_verified"] = validation.get("clock_verified", False)
                    video_info["extracted_minute"] = validation.get("extracted_minute")
                    video_info["timestamp_verified"] = validation.get("timestamp_status") == "verified"
                    video_info["timestamp_status"] = validation.get("timestamp_status", "unverified")
                    validated_videos.append(video_info)
                else:
                    rejected_count += 1
                    # Track timestamp rejections separately for stats
                    if validation.get("timestamp_status") == "rejected":
                        download_stats["timestamp_rejected"] += 1
                    log.info(workflow.logger, MODULE, "video_rejected",
                             "Video filtered by AI validation",
                             reason=validation.get('reason', 'unknown'),
                             is_soccer=validation.get('is_soccer', False),
                             is_screen_recording=validation.get('is_screen_recording', False),
                             confidence=validation.get('confidence', 0),
                             timestamp_status=validation.get('timestamp_status', 'unknown'),
                             event_id=event_id)
                    # Clean up rejected video file
                    try:
                        import os
                        os.remove(video_info["file_path"])
                    except:
                        pass
            except Exception as e:
                # FAIL-CLOSED: If validation fails after retries, REJECT the video
                validation_failed_count += 1
                log.error(workflow.logger, MODULE, "validation_failed",
                          "Validation FAILED - rejecting video",
                          error=str(e), event_id=event_id)
                try:
                    import os
                    os.remove(video_info["file_path"])
                except:
                    pass
        
        # Update stats
        download_stats["ai_rejected"] = rejected_count
        download_stats["ai_validation_failed"] = validation_failed_count
        
        log.info(workflow.logger, MODULE, "validation_complete",
                 "Validation complete", passed=len(validated_videos),
                 not_soccer=rejected_count, validation_errors=validation_failed_count,
                 event_id=event_id)
        
        # =========================================================================
        # Step 4: Generate perceptual hashes IN PARALLEL for validated videos
        # Uses heartbeat-based timeout (heartbeat every 5 frames)
        # =========================================================================
        if validated_videos:
            log.info(workflow.logger, MODULE, "hash_generation_start",
                     "Generating perceptual hashes in parallel",
                     count=len(validated_videos), event_id=event_id)
        
        async def generate_hash(video_info: dict, idx: int):
            try:
                hash_result = await workflow.execute_activity(
                    download_activities.generate_video_hash,
                    args=[video_info["file_path"], video_info.get("duration", 0)],
                    # Heartbeat-based timeout: activity heartbeats every frame
                    # 90s allows for resource contention during parallel processing
                    heartbeat_timeout=timedelta(seconds=90),
                    start_to_close_timeout=timedelta(seconds=300),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
                return {"idx": idx, "hash": hash_result.get("perceptual_hash", "")}
            except Exception as e:
                log.warning(workflow.logger, MODULE, "hash_generation_failed",
                            "Hash generation FAILED for video",
                            idx=idx, error=str(e), event_id=event_id)
                return {"idx": idx, "hash": "", "failed": True}
        
        # Execute all hash generations in parallel
        hash_tasks = [generate_hash(video_info, idx) for idx, video_info in enumerate(validated_videos)]
        hash_results = await asyncio.gather(*hash_tasks)
        
        # Apply hash results back to video_info objects
        hashes_generated = sum(1 for r in hash_results if r["hash"])
        hashes_failed = sum(1 for r in hash_results if r.get("failed"))
        
        # Update stats
        download_stats["hash_generated"] = hashes_generated
        download_stats["hash_failed"] = hashes_failed
        
        log.info(workflow.logger, MODULE, "hash_generation_complete",
                 "Hash generation complete",
                 generated=hashes_generated, total=len(validated_videos), event_id=event_id)
        
        for hash_result in hash_results:
            validated_videos[hash_result["idx"]]["perceptual_hash"] = hash_result["hash"]
        
        # =========================================================================
        # Filter out videos with no hash - they can't be deduplicated properly!
        # Videos without hashes would bypass dedup and create duplicates.
        # =========================================================================
        videos_with_hash = []
        videos_without_hash_count = 0
        for video_info in validated_videos:
            if video_info.get("perceptual_hash") and video_info["perceptual_hash"] != "dense:0.25:":
                videos_with_hash.append(video_info)
            else:
                videos_without_hash_count += 1
                log.warning(workflow.logger, MODULE, "video_no_hash",
                            "Skipping video with no hash (cannot deduplicate)",
                            url=video_info.get('source_url', 'unknown')[:60], event_id=event_id)
        
        if videos_without_hash_count > 0:
            log.warning(workflow.logger, MODULE, "filtered_no_hash",
                        "Filtered videos with no hash",
                        count=videos_without_hash_count, event_id=event_id)
        
        videos_to_upload = videos_with_hash
        download_stats["sent_to_upload"] = len(videos_to_upload)
        
        # =========================================================================
        # Step 5: Queue videos for upload via signal
        # Uses queue_videos_for_upload activity which does signal-with-start.
        # This guarantees FIFO ordering - Temporal delivers signals in order.
        # Only ONE UploadWorkflow runs per event, processing batches sequentially.
        # =========================================================================
        # ALWAYS signal UploadWorkflow - even with 0 videos
        # UploadWorkflow will check count and mark _download_complete when 10 reached
        # =========================================================================
        if videos_to_upload:
            log.info(workflow.logger, MODULE, "queuing_upload",
                     "Queuing videos for upload",
                     videos=len(videos_to_upload), event_id=event_id)
            await self._signal_upload_workflow(fixture_id, event_id, videos_to_upload, temp_dir, player_name, team_name)
            videos_uploaded = len(videos_to_upload)
            s3_urls = []  # We don't wait for upload results - it happens async in UploadWorkflow
        else:
            log.info(workflow.logger, MODULE, "no_videos_to_upload",
                     "No videos to upload (all filtered/failed)", event_id=event_id)
            videos_uploaded = 0
            s3_urls = []
            
            # Clean up temp directory since no uploads to do
            try:
                await workflow.execute_activity(
                    download_activities.cleanup_download_temp,
                    args=[temp_dir],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
            except Exception as e:
                log.warning(workflow.logger, MODULE, "cleanup_failed",
                            "Failed to cleanup temp dir", error=str(e))
            
            # STILL signal UploadWorkflow with empty list - it will check count
            await self._signal_upload_workflow(fixture_id, event_id, [], temp_dir)
        
        log.info(workflow.logger, MODULE, "workflow_complete",
                 "DownloadWorkflow COMPLETE",
                 uploaded=videos_uploaded, s3_urls=len(s3_urls), event_id=event_id)
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": videos_uploaded,
            "s3_urls": s3_urls,
        }
    
    async def _signal_upload_workflow(
        self, 
        fixture_id: int, 
        event_id: str, 
        videos: list, 
        temp_dir: str,
        player_name: str = "",
        team_name: str = ""
    ):
        """Signal UploadWorkflow with videos (or empty list for completion check)."""
        try:
            queue_result = await workflow.execute_activity(
                download_activities.queue_videos_for_upload,
                args=[
                    fixture_id,
                    event_id,
                    player_name,
                    team_name,
                    videos,  # May be empty - that's fine
                    temp_dir,
                ],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            
            if queue_result.get("status") == "queued":
                log.info(workflow.logger, MODULE, "upload_signaled",
                         "Signaled UploadWorkflow",
                         videos=len(videos), event_id=event_id)
            else:
                log.error(workflow.logger, MODULE, "upload_signal_failed",
                          "Failed to signal UploadWorkflow",
                          error=queue_result.get('error'), event_id=event_id)
        except Exception as e:
            log.error(workflow.logger, MODULE, "queue_videos_failed",
                      "queue_videos_for_upload FAILED",
                      error=str(e), event_id=event_id)

