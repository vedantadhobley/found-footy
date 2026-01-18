"""
Download Workflow - Video Download and Validation Pipeline

Orchestrates video download, validation, and hash generation:
1. download_single_video x N - Download each video individually (3 retries per video)
2. deduplicate_by_md5 - FAST MD5 dedup within batch (eliminates true duplicates)
3. validate_video_is_soccer x N - AI validation (only for MD5-unique videos)
4. generate_video_hash x N - Generate perceptual hash (only for validated videos)
5. queue_videos_for_upload - Signal UploadWorkflow for FIFO queue processing
6. increment_twitter_count - Track completion progress

Key Design: Uses signal-with-start to queue video batches to UploadWorkflow.
- queue_videos_for_upload activity uses Temporal client to signal-with-start
- Temporal guarantees signal delivery order = FIFO processing
- Only ONE UploadWorkflow per event, processes batches sequentially
- Different events can have parallel UploadWorkflows

Design Philosophy:
- Per-video retry (3 attempts with exponential backoff)
- Videos that fail all 3 attempts are logged but don't block workflow
- Parallel processing where possible (downloads, hashes)
- Comprehensive logging at every step with [DOWNLOAD] prefix
- Activity-level heartbeats for long operations (hash generation)

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


@workflow.defn
class DownloadWorkflow:
    """
    Download, validate, and hash videos, then delegate to UploadWorkflow.
    
    Uses granular activities for proper retry semantics:
    - Each video download is independent
    - Failures don't cascade (partial success preserved)
    - UploadWorkflow serializes S3 operations per event
    
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
    ) -> dict:
        """
        Execute the video download pipeline.
        
        Args:
            fixture_id: The fixture ID
            event_id: The event ID
            player_name: Player name for S3 metadata
            team_name: Team name for S3 metadata
            discovered_videos: List of videos from Twitter search (passed directly)
        
        Returns:
            Dict with videos_uploaded count and s3_urls list
        """
        workflow.logger.info(
            f"⬇️ [DOWNLOAD] STARTED | event={event_id} | "
            f"videos={len(discovered_videos) if discovered_videos else 0}"
        )
        
        # Initialize download stats for visibility into what happened in the pipeline
        download_stats = {
            "discovered": len(discovered_videos) if discovered_videos else 0,
            "downloaded": 0,
            "filtered_aspect_duration": 0,
            "download_failed": 0,
            "md5_batch_deduped": 0,
            "ai_rejected": 0,
            "ai_validation_failed": 0,
            "hash_generated": 0,
            "hash_failed": 0,
            "sent_to_upload": 0,
        }
        
        if not discovered_videos:
            workflow.logger.warning(f"⚠️ [DOWNLOAD] No videos to download | event={event_id}")
            # Still need to increment twitter count to track completion
            await self._increment_twitter_count(fixture_id, event_id)
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        workflow.logger.info(
            f"📋 [DOWNLOAD] Processing {len(discovered_videos)} videos | event={event_id}"
        )
        
        # Temp directory path with unique run ID to prevent conflicts between concurrent workflows
        # Uses /tmp/found-footy which is mounted as a shared volume across all worker replicas
        # This ensures activities on any worker can access files downloaded by other workers
        run_id = workflow.info().run_id[:8]
        temp_dir = f"/tmp/found-footy/{event_id}_{run_id}"
        
        # =========================================================================
        # Step 1: Download videos IN PARALLEL (with per-video retry)
        # 403 errors are common (rate limits, expired links) - retry 3x with backoff
        # =========================================================================
        workflow.logger.info(
            f"📥 [DOWNLOAD] Downloading {len(discovered_videos)} videos in parallel | event={event_id}"
        )
        
        download_results = []
        filtered_urls = []  # Track URLs filtered out (too short/long/vertical)
        failed_urls = []    # Track URLs that failed after 3 retries
        
        # Create download tasks for parallel execution
        async def download_video(idx: int, video: dict):
            video_url = video.get("tweet_url") or video.get("video_page_url")
            if not video_url:
                workflow.logger.warning(
                    f"⚠️ [DOWNLOAD] Video {idx}: No URL, skipping | event={event_id}"
                )
                return None
            
            try:
                result = await workflow.execute_activity(
                    download_activities.download_single_video,
                    args=[video_url, idx, event_id, temp_dir, video_url],
                    start_to_close_timeout=timedelta(seconds=45),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                        maximum_interval=timedelta(seconds=10),
                    ),
                )
                return {"idx": idx, "result": result, "url": video_url}
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [DOWNLOAD] Video {idx} FAILED after 3 retries | "
                    f"url={video_url[:50]}... | error={str(e)[:100]} | event={event_id}"
                )
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
                workflow.logger.info(
                    f"📹 [DOWNLOAD] Multi-video tweet {outcome['idx']}: {len(videos)} videos | event={event_id}"
                )
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
        
        workflow.logger.info(
            f"✅ [DOWNLOAD] Downloads complete | success={successful_downloads} | "
            f"filtered={len(filtered_urls)} | failed={len(failed_urls)} | event={event_id}"
        )
        
        # =========================================================================
        # Step 2: MD5 Dedup within batch - Fast elimination of true duplicates
        # This only deduplicates WITHIN this batch (not against S3 - that's UploadWorkflow's job)
        # =========================================================================
        successful_videos = [r for r in download_results if r.get("status") == "success"]
        
        if successful_videos:
            workflow.logger.info(
                f"🔐 [DOWNLOAD] Running fast MD5 batch dedup | videos={len(successful_videos)} | event={event_id}"
            )
            
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
                    workflow.logger.debug(
                        f"🔁 [DOWNLOAD] Batch duplicate: {video.get('source_url', '')[:50]}..."
                    )
                else:
                    seen_hashes[file_hash] = True
                    unique_videos.append(video)
            
            download_stats["md5_batch_deduped"] = batch_dupes
            successful_videos = unique_videos
            
            workflow.logger.info(
                f"✅ [DOWNLOAD] MD5 batch dedup complete | unique={len(successful_videos)} | "
                f"batch_dupes={batch_dupes} | event={event_id}"
            )
        
        # =========================================================================
        # Step 3: AI Validation (only for MD5-unique videos - saves compute!)
        # Validates videos are soccer content - rejects non-soccer early
        # =========================================================================
        validated_videos = []
        rejected_count = 0
        validation_failed_count = 0
        
        if successful_videos:
            workflow.logger.info(
                f"🔍 [DOWNLOAD] Validating {len(successful_videos)} videos with AI vision | event={event_id}"
            )
        
        for video_info in successful_videos:
            try:
                validation = await workflow.execute_activity(
                    download_activities.validate_video_is_soccer,
                    args=[video_info["file_path"], event_id],
                    start_to_close_timeout=timedelta(seconds=90),
                    retry_policy=RetryPolicy(
                        maximum_attempts=4,
                        initial_interval=timedelta(seconds=3),
                        backoff_coefficient=2.0,
                        maximum_interval=timedelta(seconds=30),
                    ),
                )
                
                if validation.get("is_valid", True):
                    validated_videos.append(video_info)
                else:
                    rejected_count += 1
                    workflow.logger.warning(
                        f"🚫 [DOWNLOAD] Video rejected (not soccer) | "
                        f"reason={validation.get('reason', 'unknown')} | event={event_id}"
                    )
                    # Clean up rejected video file
                    try:
                        import os
                        os.remove(video_info["file_path"])
                    except:
                        pass
            except Exception as e:
                # FAIL-CLOSED: If validation fails after retries, REJECT the video
                validation_failed_count += 1
                workflow.logger.error(
                    f"❌ [DOWNLOAD] Validation FAILED - rejecting video | error={e} | event={event_id}"
                )
                try:
                    import os
                    os.remove(video_info["file_path"])
                except:
                    pass
        
        # Update stats
        download_stats["ai_rejected"] = rejected_count
        download_stats["ai_validation_failed"] = validation_failed_count
        
        workflow.logger.info(
            f"✅ [DOWNLOAD] Validation complete | passed={len(validated_videos)} | "
            f"not_soccer={rejected_count} | validation_errors={validation_failed_count} | event={event_id}"
        )
        
        # =========================================================================
        # Step 4: Generate perceptual hashes IN PARALLEL for validated videos
        # Uses heartbeat-based timeout (heartbeat every 5 frames)
        # =========================================================================
        if validated_videos:
            workflow.logger.info(
                f"🔐 [DOWNLOAD] Generating perceptual hashes for {len(validated_videos)} videos in parallel | event={event_id}"
            )
        
        async def generate_hash(video_info: dict, idx: int):
            try:
                hash_result = await workflow.execute_activity(
                    download_activities.generate_video_hash,
                    args=[video_info["file_path"], video_info.get("duration", 0)],
                    # Heartbeat-based timeout: activity heartbeats every 5 frames
                    # Increased to 60s to handle parallel processing contention
                    heartbeat_timeout=timedelta(seconds=60),
                    start_to_close_timeout=timedelta(seconds=300),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
                return {"idx": idx, "hash": hash_result.get("perceptual_hash", "")}
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [DOWNLOAD] Hash generation FAILED for video {idx} | error={e} | event={event_id}"
                )
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
        
        workflow.logger.info(
            f"✅ [DOWNLOAD] Hash generation complete | generated={hashes_generated}/{len(validated_videos)} | event={event_id}"
        )
        
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
                workflow.logger.warning(
                    f"⚠️ [DOWNLOAD] Skipping video with no hash (cannot deduplicate) | "
                    f"url={video_info.get('source_url', 'unknown')[:60]}... | event={event_id}"
                )
        
        if videos_without_hash_count > 0:
            workflow.logger.warning(
                f"⚠️ [DOWNLOAD] Filtered {videos_without_hash_count} videos with no hash | event={event_id}"
            )
        
        videos_to_upload = videos_with_hash
        download_stats["sent_to_upload"] = len(videos_to_upload)
        
        # =========================================================================
        # Step 5: Queue videos for upload via signal
        # Uses queue_videos_for_upload activity which does signal-with-start.
        # This guarantees FIFO ordering - Temporal delivers signals in order.
        # Only ONE UploadWorkflow runs per event, processing batches sequentially.
        # =========================================================================
        if videos_to_upload:
            workflow.logger.info(
                f"☁️ [DOWNLOAD] Queuing videos for upload | videos={len(videos_to_upload)} | event={event_id}"
            )
            
            try:
                queue_result = await workflow.execute_activity(
                    download_activities.queue_videos_for_upload,
                    args=[
                        fixture_id,
                        event_id,
                        player_name,
                        team_name,
                        videos_to_upload,
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
                    videos_uploaded = len(videos_to_upload)  # Queued for upload
                    workflow.logger.info(
                        f"✅ [DOWNLOAD] Videos queued for upload | count={videos_uploaded} | event={event_id}"
                    )
                else:
                    workflow.logger.error(
                        f"❌ [DOWNLOAD] Failed to queue videos | error={queue_result.get('error')} | event={event_id}"
                    )
                    videos_uploaded = 0
                    
            except Exception as e:
                workflow.logger.error(
                    f"❌ [DOWNLOAD] queue_videos_for_upload FAILED | error={e} | event={event_id}"
                )
                videos_uploaded = 0
            
            s3_urls = []  # We don't wait for upload results - it happens async in UploadWorkflow
            
            # NOTE: Don't increment twitter count here!
            # UploadWorkflow will increment after processing this batch.
            # This ensures _twitter_complete isn't set until uploads are actually done.
        else:
            workflow.logger.info(
                f"⚠️ [DOWNLOAD] No videos to upload (all filtered/failed) | event={event_id}"
            )
            videos_uploaded = 0
            s3_urls = []
            
            # Clean up temp directory since we're not calling UploadWorkflow
            try:
                await workflow.execute_activity(
                    download_activities.cleanup_download_temp,
                    args=[temp_dir],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
            except Exception as e:
                workflow.logger.warning(f"⚠️ [DOWNLOAD] Failed to cleanup temp dir | error={e}")
            
            # Increment twitter count here since no upload workflow will do it
            await self._increment_twitter_count(fixture_id, event_id)
        
        workflow.logger.info(
            f"🎉 [DOWNLOAD] WORKFLOW COMPLETE | uploaded={videos_uploaded} | "
            f"s3_urls={len(s3_urls)} | event={event_id}"
        )
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": videos_uploaded,
            "s3_urls": s3_urls,
        }
    
    async def _increment_twitter_count(self, fixture_id: int, event_id: str):
        """Increment twitter count and check for completion."""
        try:
            increment_result = await workflow.execute_activity(
                download_activities.increment_twitter_count,
                args=[fixture_id, event_id, 10],  # 10 total attempts
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=5,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            if increment_result.get("marked_complete"):
                workflow.logger.info(
                    f"🏁 [DOWNLOAD] All 10 attempts complete, marked _twitter_complete=true | event={event_id}"
                )
        except Exception as e:
            workflow.logger.error(
                f"❌ [DOWNLOAD] increment_twitter_count FAILED | error={e} | event={event_id}"
            )
