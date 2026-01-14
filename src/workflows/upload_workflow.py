"""
Upload Workflow - Serialized S3 Deduplication and Upload

This workflow SERIALIZES S3 operations per event to prevent race conditions.
Multiple DownloadWorkflows may find videos for the same event simultaneously,
but only ONE UploadWorkflow runs at a time per event (Temporal guarantees this
via deterministic workflow ID: `upload-{event_id}`).

Flow:
1. DownloadWorkflow downloads, validates, and generates hashes for videos
2. DownloadWorkflow calls UploadWorkflow as a BLOCKING child workflow
3. UploadWorkflow:
   a. Fetches current S3 state (fresh, not stale)
   b. Deduplicates against S3 (MD5 + perceptual)
   c. Uploads new/replacement videos
   d. Updates MongoDB
   e. Recalculates video ranks
4. DownloadWorkflow continues after UploadWorkflow completes

The KEY insight: Temporal will queue multiple child workflow calls with the
same workflow ID. Only one runs at a time, so S3 state is always fresh.

Started by: DownloadWorkflow (BLOCKING - waits for completion)
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
import asyncio
from dataclasses import dataclass
from typing import List, Optional

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
    """Input for the UploadWorkflow."""
    fixture_id: int
    event_id: str
    player_name: str
    team_name: str
    videos: List[dict]  # List of video dicts with file_path, hashes, metadata
    temp_dir: str  # Temp directory to clean up after upload


@workflow.defn
class UploadWorkflow:
    """
    Serialized S3 upload workflow.
    
    This workflow handles the critical S3 deduplication and upload phase.
    By using a deterministic workflow ID per event, Temporal ensures only
    one instance runs at a time, eliminating race conditions.
    """
    
    @workflow.run
    async def run(self, input: UploadWorkflowInput) -> dict:
        """
        Execute serialized S3 deduplication and upload.
        
        Args:
            input: UploadWorkflowInput with fixture_id, event_id, videos, etc.
        
        Returns:
            Dict with videos_uploaded count and video_objects list
        """
        fixture_id = input.fixture_id
        event_id = input.event_id
        videos = input.videos
        
        workflow.logger.info(
            f"☁️ [UPLOAD] STARTED | event={event_id} | "
            f"videos_to_process={len(videos)}"
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
            await self._cleanup_temp_files(videos, input.temp_dir, event_id)
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
                        input.player_name,
                        input.team_name,
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
        await self._cleanup_temp_files(videos, input.temp_dir, event_id)
        
        workflow.logger.info(
            f"🎉 [UPLOAD] WORKFLOW COMPLETE | uploaded={len(video_objects)} | event={event_id}"
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
