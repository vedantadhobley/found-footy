"""
Upload Workflow - Signal-Based FIFO Queue for Serialized S3 Operations

This workflow receives video batches via SIGNALS from DownloadWorkflows.
Temporal guarantees signal delivery order, so batches are processed FIFO.

Pattern:
1. DownloadWorkflow calls queue_videos_for_upload activity
2. Activity uses Temporal client's signal-with-start:
   - If no UploadWorkflow exists: starts one AND delivers the signal
   - If one exists: just delivers the signal to the queue
3. UploadWorkflow processes signals ONE AT A TIME in FIFO order
4. After idle timeout (no new signals), workflow completes

This ensures only ONE upload runs at a time per event, and they're processed
in the order the DownloadWorkflows finished (first to signal = first to upload).

Different events have separate UploadWorkflows and can run in parallel.

Started by: queue_videos_for_upload activity (signal-with-start)
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
import asyncio
from dataclasses import dataclass
from typing import List
from collections import deque

with workflow.unsafe.imports_passed_through():
    from src.activities import upload as upload_activities
    from src.activities import monitor as monitor_activities
    from src.activities import download as download_activities


@dataclass
class UploadWorkflowInput:
    """Input for the UploadWorkflow."""
    fixture_id: int
    event_id: str
    player_name: str
    team_name: str
    videos: List[dict]  # Usually empty - videos come via signal
    temp_dir: str


@workflow.defn
class UploadWorkflow:
    """
    Signal-based FIFO queue for S3 uploads.
    
    Receives video batches via signals, processes them one at a time.
    Temporal guarantees signal ordering = FIFO processing.
    """
    
    def __init__(self):
        self._pending_batches: deque = deque()
        self._total_uploaded = 0
        self._total_batches_processed = 0
    
    @workflow.signal
    def add_videos(self, batch: dict) -> None:
        """
        Signal handler - adds a batch of videos to the FIFO queue.
        
        Called via signal-with-start from queue_videos_for_upload activity.
        Temporal guarantees signals are delivered in order.
        """
        videos = batch.get("videos", [])
        workflow.logger.info(
            f"📥 [UPLOAD] Received batch signal | videos={len(videos)} | "
            f"queue_size={len(self._pending_batches)}"
        )
        self._pending_batches.append(batch)
    
    @workflow.run
    async def run(self, input: UploadWorkflowInput) -> dict:
        """
        Main workflow - processes batches from queue in FIFO order.
        """
        fixture_id = input.fixture_id
        event_id = input.event_id
        
        workflow.logger.info(
            f"🚀 [UPLOAD] WORKFLOW STARTED | event={event_id}"
        )
        
        # Process batches until idle timeout
        idle_timeout = timedelta(minutes=5)
        
        while True:
            # Wait for a batch to process OR timeout
            try:
                await workflow.wait_condition(
                    lambda: len(self._pending_batches) > 0,
                    timeout=idle_timeout,
                )
            except asyncio.TimeoutError:
                workflow.logger.info(
                    f"⏰ [UPLOAD] Idle timeout, completing | "
                    f"total_uploaded={self._total_uploaded} | "
                    f"batches={self._total_batches_processed} | event={event_id}"
                )
                # Failsafe: Check if all download workflows registered before exiting
                # This catches edge cases where check was missed during processing
                await self._check_and_mark_twitter_complete(fixture_id, event_id)
                break
            
            # Process ONE batch at a time (FIFO)
            if self._pending_batches:
                batch = self._pending_batches.popleft()
                
                workflow.logger.info(
                    f"☁️ [UPLOAD] Processing batch | videos={len(batch.get('videos', []))} | "
                    f"queue_remaining={len(self._pending_batches)} | event={event_id}"
                )
                
                try:
                    result = await self._process_batch(
                        fixture_id=fixture_id,
                        event_id=event_id,
                        player_name=batch.get("player_name", ""),
                        team_name=batch.get("team_name", ""),
                        videos=batch.get("videos", []),
                        temp_dir=batch.get("temp_dir", ""),
                    )
                    
                    # Check if event was VAR'd - stop processing all batches
                    if result.get("terminated_early") and result.get("reason") == "event_removed":
                        workflow.logger.warning(
                            f"🛑 [UPLOAD] Event removed (VAR'd?) | "
                            f"stopping workflow | event={event_id}"
                        )
                        return {
                            "fixture_id": fixture_id,
                            "event_id": event_id,
                            "videos_uploaded": self._total_uploaded,
                            "batches_processed": self._total_batches_processed,
                            "terminated_early": True,
                            "reason": "event_removed",
                        }
                    
                    self._total_uploaded += result.get("videos_uploaded", 0)
                    self._total_batches_processed += 1
                    
                    # Check if all 10 download workflows have registered
                    # Uses workflow-ID-based tracking instead of counter increments
                    await self._check_and_mark_twitter_complete(fixture_id, event_id)
                    
                    workflow.logger.info(
                        f"✅ [UPLOAD] Batch complete | uploaded={result.get('videos_uploaded', 0)} | "
                        f"total={self._total_uploaded} | event={event_id}"
                    )
                except Exception as e:
                    workflow.logger.error(
                        f"❌ [UPLOAD] Batch FAILED | error={e} | event={event_id}"
                    )
                    self._total_batches_processed += 1
                    # Still check completion even on failure - the download workflow ran
                    await self._check_and_mark_twitter_complete(fixture_id, event_id)
        
        workflow.logger.info(
            f"🎉 [UPLOAD] WORKFLOW COMPLETE | total_uploaded={self._total_uploaded} | "
            f"batches={self._total_batches_processed} | event={event_id}"
        )
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": self._total_uploaded,
            "batches_processed": self._total_batches_processed,
        }
    
    async def _process_batch(
        self,
        fixture_id: int,
        event_id: str,
        player_name: str,
        team_name: str,
        videos: List[dict],
        temp_dir: str,
    ) -> dict:
        """
        Process a single batch of videos.
        
        This is the core upload logic - fetches fresh S3 state, deduplicates,
        uploads, and updates MongoDB.
        """
        workflow.logger.info(
            f"☁️ [UPLOAD] Processing batch | videos={len(videos)} | event={event_id}"
        )
        
        if not videos:
            workflow.logger.warning(f"⚠️ [UPLOAD] No videos to upload | event={event_id}")
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "video_objects": [],
                "s3_urls": [],
            }
        
        # =========================================================================
        # Step 1: Fetch FRESH S3 state
        # This is the key - we fetch S3 state INSIDE the serialized workflow,
        # guaranteeing no other upload can modify S3 while we're working.
        # =========================================================================
        workflow.logger.info(f"📋 [UPLOAD] Fetching fresh S3 state | event={event_id}")
        
        try:
            event_data = await workflow.execute_activity(
                upload_activities.fetch_event_data,
                args=[fixture_id, event_id],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            
            # =========================================================================
            # VAR Check: If event was removed, abort this batch (and signal workflow to stop)
            # =========================================================================
            if event_data.get("status") == "error" and event_data.get("error") == "event_not_found":
                workflow.logger.warning(
                    f"🛑 [UPLOAD] Event no longer exists (VAR'd?) | event={event_id} | ABORTING BATCH"
                )
                return {
                    "fixture_id": fixture_id,
                    "event_id": event_id,
                    "videos_uploaded": 0,
                    "video_objects": [],
                    "s3_urls": [],
                    "terminated_early": True,
                    "reason": "event_removed",
                }
            
            existing_s3_videos = event_data.get("existing_s3_videos", [])
            workflow.logger.info(
                f"✅ [UPLOAD] Got fresh S3 state | existing_videos={len(existing_s3_videos)} | event={event_id}"
            )
        except Exception as e:
            workflow.logger.error(f"❌ [UPLOAD] fetch_event_data FAILED | error={e} | event={event_id}")
            existing_s3_videos = []
        
        # =========================================================================
        # Step 2: MD5 Deduplication against S3
        # Fast check for exact file matches
        # =========================================================================
        workflow.logger.info(
            f"🔐 [UPLOAD] Running MD5 dedup | videos={len(videos)} | event={event_id}"
        )
        
        try:
            md5_result = await workflow.execute_activity(
                upload_activities.deduplicate_by_md5,
                args=[videos, existing_s3_videos],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            
            # Handle S3 exact matches - bump popularity
            s3_exact_matches = md5_result.get("s3_exact_matches", [])
            if s3_exact_matches:
                workflow.logger.info(
                    f"📈 [UPLOAD] Bumping popularity for {len(s3_exact_matches)} MD5-matched S3 videos | event={event_id}"
                )
                for match in s3_exact_matches:
                    try:
                        await workflow.execute_activity(
                            upload_activities.bump_video_popularity,
                            args=[
                                fixture_id,
                                event_id,
                                match["s3_video"].get("s3_url", ""),
                                match["new_popularity"],
                            ],
                            start_to_close_timeout=timedelta(seconds=15),
                            retry_policy=RetryPolicy(maximum_attempts=2),
                        )
                    except Exception as e:
                        workflow.logger.warning(
                            f"⚠️ [UPLOAD] Failed to bump MD5-match popularity | error={e}"
                        )
            
            # Handle MD5 S3 replacements - queue for later
            md5_s3_replacements = md5_result.get("s3_replacements", [])
            
            # Continue with unique videos
            videos_after_md5 = md5_result.get("unique_videos", [])
            md5_dupes_removed = md5_result.get("md5_duplicates_removed", 0)
            
            workflow.logger.info(
                f"✅ [UPLOAD] MD5 dedup complete | unique={len(videos_after_md5)} | "
                f"batch_dupes={md5_dupes_removed} | s3_matches={len(s3_exact_matches)} | "
                f"s3_replacements={len(md5_s3_replacements)} | event={event_id}"
            )
        except Exception as e:
            workflow.logger.error(f"❌ [UPLOAD] MD5 dedup FAILED | error={e} | event={event_id}")
            videos_after_md5 = videos
            md5_s3_replacements = []
        
        # Add MD5 S3 replacements to the list
        for replacement in md5_s3_replacements:
            replacement["new_video"]["_is_md5_replacement"] = True
            replacement["new_video"]["_old_s3_video"] = replacement["old_s3_video"]
            videos_after_md5.append(replacement["new_video"])
        
        # =========================================================================
        # Step 3: Perceptual Hash Deduplication against S3
        # Separates MD5 replacements (already matched) from perceptual dedup
        # =========================================================================
        md5_replacement_videos = []
        perceptual_dedup_videos = []
        for video in videos_after_md5:
            if video.get("_is_md5_replacement"):
                md5_replacement_videos.append({
                    "new_video": video,
                    "old_s3_video": video.get("_old_s3_video", {}),
                })
            else:
                perceptual_dedup_videos.append(video)
        
        workflow.logger.info(
            f"🔍 [UPLOAD] Running perceptual dedup | videos={len(perceptual_dedup_videos)} | event={event_id}"
        )
        
        try:
            dedup_result = await workflow.execute_activity(
                upload_activities.deduplicate_videos,
                args=[perceptual_dedup_videos, existing_s3_videos],
                # Heartbeat-based timeout: activity heartbeats every video comparison.
                # 120s heartbeat allows time for complex hash comparisons between heartbeats.
                # 1 hour start_to_close is a safety ceiling - heartbeat is the real control.
                heartbeat_timeout=timedelta(seconds=120),
                start_to_close_timeout=timedelta(hours=1),
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
        except Exception as e:
            # CRITICAL: Do NOT upload videos as new when dedup fails!
            # That's what caused the duplicate video bug. Instead, skip them entirely.
            # The videos will be retried in the next download batch.
            workflow.logger.error(
                f"❌ [UPLOAD] Perceptual dedup FAILED - SKIPPING BATCH to avoid duplicates | "
                f"error={e} | videos={len(perceptual_dedup_videos)} | event={event_id}"
            )
            dedup_result = {
                "videos_to_upload": [],  # EMPTY - don't upload anything!
                "videos_to_replace": [],
                "videos_to_bump_popularity": [],
                "skipped_urls": [v.get("source_url", "") for v in perceptual_dedup_videos],
            }
        
        videos_to_upload = dedup_result.get("videos_to_upload", [])
        videos_to_replace = dedup_result.get("videos_to_replace", []) + md5_replacement_videos
        videos_to_bump_popularity = dedup_result.get("videos_to_bump_popularity", [])
        skipped_urls = dedup_result.get("skipped_urls", [])
        
        total_to_process = len(videos_to_upload) + len(videos_to_replace)
        workflow.logger.info(
            f"✅ [UPLOAD] Perceptual dedup complete | new={len(videos_to_upload)} | "
            f"replacements={len(videos_to_replace)} | skipped={len(skipped_urls)} | event={event_id}"
        )
        
        # =========================================================================
        # Step 4: Bump popularity for existing videos
        # (when we skipped uploading because existing was higher quality)
        # =========================================================================
        if videos_to_bump_popularity:
            workflow.logger.info(
                f"📈 [UPLOAD] Bumping popularity for {len(videos_to_bump_popularity)} existing videos | event={event_id}"
            )
            
            async def bump_popularity(bump_info: dict):
                s3_video = bump_info["s3_video"]
                new_popularity = bump_info["new_popularity"]
                try:
                    await workflow.execute_activity(
                        upload_activities.bump_video_popularity,
                        args=[
                            fixture_id,
                            event_id,
                            s3_video.get("s3_url", ""),
                            new_popularity,
                        ],
                        start_to_close_timeout=timedelta(seconds=15),
                        retry_policy=RetryPolicy(
                            maximum_attempts=2,
                            initial_interval=timedelta(seconds=1),
                        ),
                    )
                except Exception as e:
                    workflow.logger.warning(
                        f"⚠️ [UPLOAD] Failed to bump popularity | error={e} | event={event_id}"
                    )
            
            bump_tasks = [bump_popularity(info) for info in videos_to_bump_popularity]
            await asyncio.gather(*bump_tasks)
        
        if total_to_process == 0:
            workflow.logger.info(
                f"⚠️ [UPLOAD] No videos to upload (all duplicates) | event={event_id}"
            )
            # NOTE: Don't clean up temp dir here - TwitterWorkflow handles cleanup
            # Individual files may be needed by other batches from same download run
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "video_objects": [],
                "s3_urls": [],
            }
        
        # =========================================================================
        # Step 5: Prepare replacements (but DON'T remove MongoDB entries yet!)
        # We'll only remove old entries AFTER successful upload
        # =========================================================================
        if videos_to_replace:
            workflow.logger.info(
                f"♻️ [UPLOAD] Preparing {len(videos_to_replace)} video replacements | event={event_id}"
            )
            # Store the replacement info - we'll remove old entries AFTER successful upload
        
        # =========================================================================
        # Step 6: Upload videos to S3 IN PARALLEL
        # =========================================================================
        all_uploads = videos_to_upload + [r["new_video"] for r in videos_to_replace]
        workflow.logger.info(
            f"☁️ [UPLOAD] Uploading {len(all_uploads)} videos to S3 | event={event_id}"
        )
        
        async def upload_video(idx: int, video_info: dict):
            try:
                existing_s3_key = video_info.get("_old_s3_key", "")
                
                result = await workflow.execute_activity(
                    upload_activities.upload_single_video,
                    args=[
                        video_info["file_path"],
                        fixture_id,
                        event_id,
                        player_name,
                        team_name,
                        idx,
                        video_info.get("file_hash", ""),
                        video_info.get("perceptual_hash", ""),
                        video_info.get("duration", 0.0),
                        video_info.get("popularity", 1),
                        "",  # assister_name
                        "",  # opponent_team
                        video_info.get("source_url", ""),
                        video_info.get("width", 0),
                        video_info.get("height", 0),
                        video_info.get("bitrate", 0.0),
                        video_info.get("file_size", 0),
                        existing_s3_key,
                    ],
                    start_to_close_timeout=timedelta(seconds=60),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=1.5,
                    ),
                )
                return {"idx": idx, "result": result, "video_info": video_info}
            except Exception as e:
                workflow.logger.error(
                    f"❌ [UPLOAD] Upload {idx} FAILED | error={str(e)[:100]} | event={event_id}"
                )
                return {"idx": idx, "result": None, "error": str(e)}
        
        upload_tasks = [upload_video(idx, video_info) for idx, video_info in enumerate(all_uploads)]
        upload_outcomes = await asyncio.gather(*upload_tasks)
        
        video_objects = []
        s3_urls = []
        successful_replacements = []  # Track which replacements succeeded
        
        # Split results: first len(videos_to_upload) are new uploads, rest are replacements
        num_new_uploads = len(videos_to_upload)
        
        for i, outcome in enumerate(upload_outcomes):
            result = outcome.get("result")
            if result and result.get("status") == "success":
                video_obj = result.get("video_object", {
                    "url": result["s3_url"],
                    "perceptual_hash": result.get("perceptual_hash", ""),
                    "resolution_score": result.get("resolution_score", 0),
                    "file_size": 0,
                    "popularity": result.get("popularity", 1),
                    "rank": 0,
                })
                s3_urls.append(result["s3_url"])
                
                # Track successful replacements (indices >= num_new_uploads are replacements)
                if i >= num_new_uploads:
                    replacement_idx = i - num_new_uploads
                    # Store the new video object with the replacement info
                    replacement_info = videos_to_replace[replacement_idx].copy()
                    replacement_info["new_video_object"] = video_obj
                    successful_replacements.append(replacement_info)
                else:
                    # Only add to video_objects if it's a NEW upload (not a replacement)
                    video_objects.append(video_obj)
        
        workflow.logger.info(
            f"✅ [UPLOAD] Uploads complete | success={len(video_objects)}/{len(all_uploads)} | event={event_id}"
        )
        
        # =========================================================================
        # Step 7: Update MongoDB with video objects
        # =========================================================================
        if video_objects:
            workflow.logger.info(
                f"💾 [UPLOAD] Saving {len(video_objects)} video objects to MongoDB | event={event_id}"
            )
            
            try:
                await workflow.execute_activity(
                    upload_activities.save_video_objects,
                    args=[fixture_id, event_id, video_objects],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                    ),
                )
                workflow.logger.info(f"✅ [UPLOAD] Saved to MongoDB | event={event_id}")
            except Exception as e:
                workflow.logger.error(
                    f"❌ [UPLOAD] save_video_objects FAILED | error={e} | event={event_id}"
                )
        
        # =========================================================================
        # Step 7b: Atomic in-place update for SUCCESSFUL replacements
        # Since we reuse the same S3 key/URL, we update the existing MongoDB entry in-place
        # This avoids the race condition where video disappears between remove and add
        # =========================================================================
        if successful_replacements:
            workflow.logger.info(
                f"♻️ [UPLOAD] Updating {len(successful_replacements)} videos in-place | event={event_id}"
            )
            
            async def update_video_in_place(replacement: dict):
                old_video = replacement["old_s3_video"]
                new_video_obj = replacement["new_video_object"]
                try:
                    await workflow.execute_activity(
                        upload_activities.update_video_in_place,
                        args=[
                            fixture_id,
                            event_id,
                            old_video.get("s3_url", ""),  # URL stays the same
                            new_video_obj,  # New metadata
                        ],
                        start_to_close_timeout=timedelta(seconds=30),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=2),
                            backoff_coefficient=2.0,
                        ),
                    )
                except Exception as e:
                    workflow.logger.warning(
                        f"⚠️ [UPLOAD] Failed to update video in-place | "
                        f"url={old_video.get('s3_url', '').split('/')[-1]} | error={e} | event={event_id}"
                    )
            
            update_tasks = [update_video_in_place(r) for r in successful_replacements]
            await asyncio.gather(*update_tasks)
        
        # =========================================================================
        # Step 8: Recalculate video ranks
        # =========================================================================
        try:
            await workflow.execute_activity(
                upload_activities.recalculate_video_ranks,
                args=[fixture_id, event_id],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            workflow.logger.info(f"✅ [UPLOAD] Recalculated video ranks | event={event_id}")
        except Exception as e:
            workflow.logger.warning(
                f"⚠️ [UPLOAD] Failed to recalculate ranks (non-critical) | error={e} | event={event_id}"
            )
        
        
        # =========================================================================
        # Step 9: Notify frontend (for both new videos AND in-place updates)
        # =========================================================================
        videos_changed = len(video_objects) + len(successful_replacements)
        if videos_changed > 0:
            try:
                await workflow.execute_activity(
                    monitor_activities.notify_frontend_refresh,
                    start_to_close_timeout=timedelta(seconds=15),
                    retry_policy=RetryPolicy(maximum_attempts=1),
                )
                workflow.logger.info(f"📡 [UPLOAD] Frontend notified | event={event_id}")
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [UPLOAD] Frontend notification failed (non-critical) | error={e} | event={event_id}"
                )
        
        # =========================================================================
        # Step 10: Cleanup individual uploaded files (not entire temp dir)
        # The temp directory is cleaned up by TwitterWorkflow when all 10 attempts complete
        # =========================================================================
        await self._cleanup_uploaded_files(all_uploads, s3_urls, event_id)
        
        workflow.logger.info(
            f"✅ [UPLOAD] Batch uploaded={len(video_objects)} | event={event_id}"
        )
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": len(video_objects),
            "video_objects": video_objects,
            "s3_urls": s3_urls,
        }
    
    async def _cleanup_uploaded_files(self, all_uploads: list, successful_urls: list, event_id: str):
        """
        Clean up individual files that were successfully uploaded.
        Only deletes files after successful S3 upload - leaves others for retry.
        Does NOT delete the temp directory - that's handled by TwitterWorkflow.
        """
        if not successful_urls:
            return
        
        # Find file paths for successfully uploaded videos
        files_to_delete = []
        for video in all_uploads:
            # Check if this video was successfully uploaded
            file_path = video.get("file_path", "")
            if file_path and any(url for url in successful_urls if video.get("file_hash", "xxx") in url or video.get("perceptual_hash", "xxx")[:8] in url):
                files_to_delete.append(file_path)
        
        if files_to_delete:
            try:
                await workflow.execute_activity(
                    upload_activities.cleanup_individual_files,
                    args=[files_to_delete],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
                workflow.logger.info(f"🧹 [UPLOAD] Cleaned up {len(files_to_delete)} uploaded files | event={event_id}")
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [UPLOAD] Failed to cleanup files (non-critical) | error={e} | event={event_id}"
                )

    async def _check_and_mark_twitter_complete(self, fixture_id: int, event_id: str):
        """
        Check if all 10 download workflows have registered and mark _twitter_complete if so.
        
        Uses workflow-ID-based tracking: counts entries in _download_workflows array.
        This is idempotent - calling it multiple times is safe.
        """
        try:
            result = await workflow.execute_activity(
                download_activities.check_and_mark_twitter_complete,
                args=[fixture_id, event_id, 10],  # 10 total attempts required
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=5,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            if result.get("marked_complete"):
                workflow.logger.info(
                    f"🏁 [UPLOAD] All 10 download workflows registered, marked _twitter_complete=true | event={event_id}"
                )
            else:
                count = result.get("download_count", "?")
                workflow.logger.info(
                    f"📊 [UPLOAD] Download workflow count checked | count={count}/10 | event={event_id}"
                )
        except Exception as e:
            workflow.logger.error(
                f"❌ [UPLOAD] check_and_mark_twitter_complete FAILED | error={e} | event={event_id}"
            )
