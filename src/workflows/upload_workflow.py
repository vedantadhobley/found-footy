"""
Upload Workflow - Signal-Based Queue for Serialized S3 Operations

This workflow uses Temporal SIGNALS to queue video upload batches.
Multiple DownloadWorkflows can signal videos to the same UploadWorkflow,
and the workflow processes them ONE BATCH AT A TIME in order.

Pattern: Signal-With-Start
1. DownloadWorkflow uses signal_child_workflow() to send videos
2. If no UploadWorkflow exists for that event, one is started
3. If one already exists, videos are added to its queue
4. UploadWorkflow processes queue items serially (no race conditions!)
5. When queue is empty and idle timeout reached, workflow completes

Why signals instead of child workflows?
- Child workflows with same ID FAIL if one is already running
- Signals ADD WORK to a running workflow
- Perfect for "queue work to a single processor per event"

Started by: DownloadWorkflow via signal_child_workflow() / Signal-With-Start
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
import asyncio
from dataclasses import dataclass, field
from typing import List, Optional
from collections import deque

with workflow.unsafe.imports_passed_through():
    from src.activities import upload as upload_activities
    from src.activities import monitor as monitor_activities


@dataclass
class VideoToUpload:
    """A video that has been downloaded, validated, and hashed - ready for upload."""
    file_path: str
    source_url: str
    file_hash: str  # MD5 hash
    perceptual_hash: str
    duration: float
    popularity: int
    width: int
    height: int
    bitrate: float
    file_size: int


@dataclass
class UploadWorkflowInput:
    """Input for the UploadWorkflow - initial batch of videos."""
    fixture_id: int
    event_id: str
    player_name: str
    team_name: str
    videos: List[dict]  # List of video dicts with file_path, hashes, metadata
    temp_dir: str  # Temp directory to clean up after upload


@dataclass
class UploadBatchSignal:
    """Signal payload for adding a batch of videos to the upload queue."""
    player_name: str
    team_name: str
    videos: List[dict]
    temp_dir: str


@workflow.defn
class UploadWorkflow:
    """
    Signal-based queued S3 upload workflow.
    
    Uses Temporal signals to queue video batches from multiple DownloadWorkflows.
    Processes batches ONE AT A TIME (serialized) to prevent S3 race conditions.
    
    Pattern:
    - First batch comes via workflow input (from signal-with-start)
    - Additional batches arrive via add_videos signal
    - Workflow processes queue until empty, then waits for more or times out
    """
    
    def __init__(self):
        # Queue of batches to process
        self._pending_batches: deque = deque()
        # Track if we're currently processing (for wait_condition)
        self._processing = False
        # Total stats across all batches
        self._total_uploaded = 0
        self._total_batches_processed = 0
    
    @workflow.signal
    def add_videos(self, batch: dict) -> None:
        """
        Signal handler to add a batch of videos to the upload queue.
        
        Called by DownloadWorkflow when it has videos ready for upload.
        The batch is queued and will be processed after current work completes.
        """
        workflow.logger.info(
            f"📥 [UPLOAD] Received signal with {len(batch.get('videos', []))} videos | "
            f"queue_size={len(self._pending_batches)}"
        )
        self._pending_batches.append(batch)
    
    @workflow.run
    async def run(self, input: UploadWorkflowInput) -> dict:
        """
        Main workflow loop - processes video batches from queue.
        
        Args:
            input: Initial batch from signal-with-start
        
        Returns:
            Dict with total videos uploaded across all batches
        """
        fixture_id = input.fixture_id
        event_id = input.event_id
        
        workflow.logger.info(
            f"🚀 [UPLOAD] WORKFLOW STARTED | event={event_id} | "
            f"initial_videos={len(input.videos)}"
        )
        
        # Add initial batch to queue (from the signal-with-start)
        if input.videos:
            self._pending_batches.append({
                "player_name": input.player_name,
                "team_name": input.team_name,
                "videos": input.videos,
                "temp_dir": input.temp_dir,
            })
        
        # Process batches until queue is empty and we've been idle for a while
        # The workflow will keep running and accepting signals for new batches
        idle_timeout = timedelta(minutes=5)  # Stay alive for 5 min after last batch
        
        while True:
            # Wait for either: a batch to process OR timeout if queue is empty
            try:
                await workflow.wait_condition(
                    lambda: len(self._pending_batches) > 0,
                    timeout=idle_timeout,
                )
            except asyncio.TimeoutError:
                # No new batches for idle_timeout - we can complete
                workflow.logger.info(
                    f"⏰ [UPLOAD] Idle timeout reached, completing | "
                    f"total_uploaded={self._total_uploaded} | "
                    f"batches_processed={self._total_batches_processed} | event={event_id}"
                )
                break
            
            # Process all pending batches
            while self._pending_batches:
                batch = self._pending_batches.popleft()
                self._processing = True
                
                try:
                    result = await self._process_batch(
                        fixture_id=fixture_id,
                        event_id=event_id,
                        player_name=batch["player_name"],
                        team_name=batch["team_name"],
                        videos=batch["videos"],
                        temp_dir=batch["temp_dir"],
                    )
                    
                    self._total_uploaded += result.get("videos_uploaded", 0)
                    self._total_batches_processed += 1
                    
                    workflow.logger.info(
                        f"✅ [UPLOAD] Batch complete | batch_uploaded={result.get('videos_uploaded', 0)} | "
                        f"total_uploaded={self._total_uploaded} | "
                        f"queue_remaining={len(self._pending_batches)} | event={event_id}"
                    )
                except Exception as e:
                    workflow.logger.error(
                        f"❌ [UPLOAD] Batch FAILED | error={e} | event={event_id}"
                    )
                    self._total_batches_processed += 1
                finally:
                    self._processing = False
        
        workflow.logger.info(
            f"🎉 [UPLOAD] WORKFLOW COMPLETE | total_uploaded={self._total_uploaded} | "
            f"batches_processed={self._total_batches_processed} | event={event_id}"
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
                start_to_close_timeout=timedelta(seconds=60),
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
        except Exception as e:
            workflow.logger.error(f"❌ [UPLOAD] Perceptual dedup FAILED | error={e} | event={event_id}")
            dedup_result = {
                "videos_to_upload": perceptual_dedup_videos,
                "videos_to_replace": [],
                "videos_to_bump_popularity": [],
                "skipped_urls": [],
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
            # Clean up temp files even if nothing to upload
            await self._cleanup_temp_files(videos, temp_dir, event_id)
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "video_objects": [],
                "s3_urls": [],
            }
        
        # =========================================================================
        # Step 5: Handle replacements - remove old MongoDB entries
        # S3 files will be overwritten with same key
        # =========================================================================
        if videos_to_replace:
            workflow.logger.info(
                f"♻️ [UPLOAD] Preparing {len(videos_to_replace)} video replacements | event={event_id}"
            )
            
            async def remove_old_mongo_entry(replacement: dict):
                old_video = replacement["old_s3_video"]
                try:
                    await workflow.execute_activity(
                        upload_activities.replace_s3_video,
                        args=[
                            fixture_id,
                            event_id,
                            old_video.get("s3_url", ""),
                            old_video.get("_s3_key", ""),
                            True,  # skip_s3_delete - we'll overwrite
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
                        f"⚠️ [UPLOAD] Failed to remove old MongoDB entry | "
                        f"key={old_video.get('_s3_key')} | error={e} | event={event_id}"
                    )
            
            delete_tasks = [remove_old_mongo_entry(r) for r in videos_to_replace]
            await asyncio.gather(*delete_tasks)
        
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
        
        for outcome in upload_outcomes:
            result = outcome.get("result")
            if result and result.get("status") == "success":
                video_objects.append(result.get("video_object", {
                    "url": result["s3_url"],
                    "perceptual_hash": result.get("perceptual_hash", ""),
                    "resolution_score": result.get("resolution_score", 0),
                    "file_size": 0,
                    "popularity": result.get("popularity", 1),
                    "rank": 0,
                }))
                s3_urls.append(result["s3_url"])
        
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
        # Step 9: Notify frontend
        # =========================================================================
        if video_objects:
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
        # Step 10: Cleanup temp files
        # =========================================================================
        await self._cleanup_temp_files(videos, temp_dir, event_id)
        
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
    
    async def _cleanup_temp_files(self, videos: list, temp_dir: str, event_id: str):
        """Clean up temporary video files."""
        try:
            await workflow.execute_activity(
                upload_activities.cleanup_upload_temp,
                args=[temp_dir],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(maximum_attempts=2),
            )
            workflow.logger.info(f"🧹 [UPLOAD] Cleaned up temp directory | event={event_id}")
        except Exception as e:
            workflow.logger.warning(
                f"⚠️ [UPLOAD] Failed to cleanup temp dir (non-critical) | error={e} | event={event_id}"
            )
