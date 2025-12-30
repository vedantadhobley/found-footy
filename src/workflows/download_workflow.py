"""
Download Workflow - Video Download/Upload Pipeline

Orchestrates granular download/upload with per-video retry:
1. fetch_event_data - Get existing S3 video metadata for quality comparison
2. download_single_video x N - Download each video individually (3 retries per video)
3. validate_video_is_soccer x N - AI validation BEFORE hash generation (saves compute)
4. generate_video_hash x N - Generate perceptual hash only for validated videos
5. deduplicate_videos - Hash dedup with quality comparison against existing S3
6. replace_s3_video x N - Delete old S3 videos being replaced by higher quality
7. upload_single_video x N - Upload new/replacement videos to S3
8. mark_download_complete - Update MongoDB, cleanup temp dir

Per-video retry (3 attempts):
- Each video gets 3 download attempts with exponential backoff (2s, 4s, 8s)
- Videos that fail all 3 attempts are marked as "failed" and skipped
- URLs are already saved to _discovered_videos by TwitterWorkflow BEFORE download starts

Optimized Pipeline Order:
- AI validation happens BEFORE perceptual hash generation
- This saves expensive hash computation for non-soccer videos
- Hash generation is dense sampling at 0.25s intervals (CPU intensive)

URL Tracking (handled by TwitterWorkflow):
- TwitterWorkflow saves ALL discovered URLs IMMEDIATELY after Twitter search
- This prevents re-discovery even if DownloadWorkflow crashes or fails
- Failed downloads won't be retried in future Twitter searches

Cross-retry quality comparison:
- New videos that match existing S3 by perceptual hash are compared by quality
- Higher quality new videos REPLACE existing S3 videos (resolution > bitrate > file_size)
- Lower quality matches are SKIPPED (URL already tracked)
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
import asyncio

with workflow.unsafe.imports_passed_through():
    from src.activities import download as download_activities
    from src.activities import monitor as monitor_activities

@workflow.defn
class DownloadWorkflow:
    """
    Download, deduplicate, and upload videos to S3.
    
    Uses granular activities for proper retry semantics:
    - Each video download/upload is independent
    - Failures don't cascade (partial success preserved)
    - Cross-retry quality comparison replaces lower quality S3 videos
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
        workflow.logger.info(f"⬇️ Starting download for {event_id}")
        
        # =========================================================================
        # Step 1: Get existing S3 video metadata for quality comparison
        # =========================================================================
        event_data = await workflow.execute_activity(
            download_activities.fetch_event_data,
            args=[fixture_id, event_id],
            start_to_close_timeout=timedelta(seconds=15),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        if not discovered_videos:
            workflow.logger.warning(f"⚠️ No videos to download")
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        existing_s3_videos = event_data.get("existing_s3_videos", [])
        
        workflow.logger.info(f"📋 Found {len(discovered_videos)} videos to download")
        if existing_s3_videos:
            workflow.logger.info(f"📦 Will compare against {len(existing_s3_videos)} existing S3 videos")
        
        # Temp directory path with unique run ID to prevent conflicts between concurrent workflows
        # Each DownloadWorkflow run gets its own temp directory
        run_id = workflow.info().run_id[:8]  # First 8 chars of workflow run ID
        temp_dir = f"/tmp/footy_{event_id}_{run_id}"
        
        # =========================================================================
        # Step 2: Download videos IN PARALLEL (with per-video retry)
        # 403 errors are common (rate limits, expired links) - retry 3x with backoff
        # Also track filtered URLs (too short/long) for dedup
        # Multi-video tweets: each tweet may return multiple videos
        # =========================================================================
        workflow.logger.info(f"📥 Downloading {len(discovered_videos)} videos in parallel...")
        
        download_results = []
        filtered_urls = []  # Track URLs filtered out (too short/long/vertical)
        failed_urls = []    # Track URLs that failed after 3 retries (don't retry again)
        
        # Create download tasks for parallel execution
        async def download_video(idx: int, video: dict):
            video_url = video.get("tweet_url") or video.get("video_page_url")
            if not video_url:
                workflow.logger.warning(f"⚠️ Video {idx}: No URL, skipping")
                return None
            
            try:
                result = await workflow.execute_activity(
                    download_activities.download_single_video,
                    args=[video_url, idx, event_id, temp_dir, video_url],
                    start_to_close_timeout=timedelta(seconds=90),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                        maximum_interval=timedelta(seconds=10),
                    ),
                )
                return {"idx": idx, "result": result, "url": video_url}
            except Exception as e:
                workflow.logger.warning(f"⚠️ Video {idx} failed after 3 retries: {str(e)[:100]}")
                return {"idx": idx, "result": {"status": "failed", "error": str(e)[:200], "source_url": video_url}, "url": video_url, "failed": True}
        
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
                workflow.logger.info(f"📹 Multi-video tweet {outcome['idx']}: {len(videos)} videos")
                download_results.extend(videos)
            else:
                download_results.append(result)
            
            # Track filtered videos (too short/long/vertical) for dedup
            if result.get("status") == "filtered":
                source_url = result.get("source_url")
                if source_url:
                    filtered_urls.append(source_url)
                    workflow.logger.info(f"📝 Tracking filtered URL for dedup: {result.get('reason')}")
            
            # Track failed URLs
            if outcome.get("failed"):
                failed_urls.append(outcome["url"])
        
        successful_downloads = sum(1 for r in download_results if r.get("status") == "success")
        filtered_count = len(filtered_urls)
        failed_count = len(failed_urls)
        workflow.logger.info(f"📥 Downloaded {successful_downloads}/{len(discovered_videos)} videos ({filtered_count} filtered, {failed_count} failed)")
        
        # =========================================================================
        # Step 3: AI Validation BEFORE hash generation (saves compute)
        # Validates videos are soccer content - rejects non-soccer early
        # =========================================================================
        successful_videos = [r for r in download_results if r.get("status") == "success"]
        validated_videos = []
        rejected_count = 0
        validation_failed_count = 0
        
        if successful_videos:
            workflow.logger.info(f"🔍 Validating {len(successful_videos)} videos with AI vision...")
        
        for video_info in successful_videos:
            try:
                validation = await workflow.execute_activity(
                    download_activities.validate_video_is_soccer,
                    args=[video_info["file_path"], event_id],
                    start_to_close_timeout=timedelta(seconds=60),  # Vision model can be slow
                    retry_policy=RetryPolicy(
                        maximum_attempts=4,  # More retries for transient failures
                        initial_interval=timedelta(seconds=3),
                        backoff_coefficient=2.0,  # 3s, 6s, 12s, 24s
                        maximum_interval=timedelta(seconds=30),
                    ),
                )
                
                if validation.get("is_valid", True):
                    validated_videos.append(video_info)
                else:
                    rejected_count += 1
                    workflow.logger.warning(
                        f"🚫 Video rejected (not soccer): {validation.get('reason', 'unknown')}"
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
                workflow.logger.error(f"🚫 Validation FAILED - rejecting video: {e}")
                try:
                    import os
                    os.remove(video_info["file_path"])
                except:
                    pass
        
        if rejected_count > 0 or validation_failed_count > 0:
            workflow.logger.info(
                f"🔍 Validation: {len(validated_videos)} passed, "
                f"{rejected_count} not soccer, {validation_failed_count} validation errors"
            )
        
        # =========================================================================
        # Step 4: Generate perceptual hashes IN PARALLEL for validated videos
        # This is CPU-intensive (0.25s sampling) - parallelism helps significantly
        # =========================================================================
        if validated_videos:
            workflow.logger.info(f"🔐 Generating perceptual hashes for {len(validated_videos)} validated videos in parallel...")
        
        async def generate_hash(video_info: dict, idx: int):
            try:
                hash_result = await workflow.execute_activity(
                    download_activities.generate_video_hash,
                    args=[video_info["file_path"], video_info.get("duration", 0)],
                    start_to_close_timeout=timedelta(seconds=60),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
                return {"idx": idx, "hash": hash_result.get("perceptual_hash", "")}
            except Exception as e:
                workflow.logger.warning(f"⚠️ Hash generation failed for video {idx}: {e}")
                return {"idx": idx, "hash": ""}
        
        # Execute all hash generations in parallel
        hash_tasks = [generate_hash(video_info, idx) for idx, video_info in enumerate(validated_videos)]
        hash_results = await asyncio.gather(*hash_tasks)
        
        # Apply hash results back to video_info objects
        for hash_result in hash_results:
            validated_videos[hash_result["idx"]]["perceptual_hash"] = hash_result["hash"]
        
        # =========================================================================
        # Step 5: Deduplicate with quality comparison
        # Returns: videos_to_upload, videos_to_replace, skipped_urls
        # =========================================================================
        dedup_result = await workflow.execute_activity(
            download_activities.deduplicate_videos,
            args=[validated_videos, existing_s3_videos],
            start_to_close_timeout=timedelta(seconds=30),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        videos_to_upload = dedup_result.get("videos_to_upload", [])
        videos_to_replace = dedup_result.get("videos_to_replace", [])
        videos_to_bump_popularity = dedup_result.get("videos_to_bump_popularity", [])
        skipped_urls = dedup_result.get("skipped_urls", [])
        
        total_to_process = len(videos_to_upload) + len(videos_to_replace)
        
        # URLs to track for dedup (even if we don't upload anything)
        all_processed_urls = skipped_urls + filtered_urls + failed_urls
        
        # =========================================================================
        # Step 3b: Bump popularity for existing videos IN PARALLEL
        # (when we skipped uploading because existing was higher quality)
        # =========================================================================
        if videos_to_bump_popularity:
            workflow.logger.info(f"📈 Bumping popularity for {len(videos_to_bump_popularity)} existing videos in parallel")
            
            async def bump_popularity(bump_info: dict):
                s3_video = bump_info["s3_video"]
                new_popularity = bump_info["new_popularity"]
                try:
                    await workflow.execute_activity(
                        download_activities.bump_video_popularity,
                        args=[
                            fixture_id,
                            event_id,
                            s3_video.get("s3_url", ""),
                            new_popularity,
                        ],
                        start_to_close_timeout=timedelta(seconds=10),
                        retry_policy=RetryPolicy(
                            maximum_attempts=2,
                            initial_interval=timedelta(seconds=1),
                        ),
                    )
                except Exception as e:
                    workflow.logger.warning(f"⚠️ Failed to bump popularity: {e}")
            
            bump_tasks = [bump_popularity(info) for info in videos_to_bump_popularity]
            await asyncio.gather(*bump_tasks)
        
        if total_to_process == 0:
            # No videos to upload - URLs already saved by TwitterWorkflow
            workflow.logger.info(f"⚠️ No videos to upload ({len(all_processed_urls)} processed)")
            
            await workflow.execute_activity(
                download_activities.mark_download_complete,
                args=[fixture_id, event_id, [], temp_dir],
                start_to_close_timeout=timedelta(seconds=10),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    backoff_coefficient=2.0,
                ),
            )
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        workflow.logger.info(
            f"🔄 Dedup results: {len(videos_to_upload)} new, "
            f"{len(videos_to_replace)} replacements, {len(skipped_urls)} skipped"
        )
        
        # =========================================================================
        # Step 6: Handle replacements - delete old S3 videos IN PARALLEL
        # =========================================================================
        if videos_to_replace:
            async def delete_old_video(replacement: dict):
                old_video = replacement["old_s3_video"]
                workflow.logger.info(f"🗑️ Deleting old S3 video: {old_video.get('s3_key')}")
                try:
                    await workflow.execute_activity(
                        download_activities.replace_s3_video,
                        args=[
                            fixture_id,
                            event_id,
                            old_video.get("s3_url", ""),
                            old_video.get("s3_key", ""),
                        ],
                        start_to_close_timeout=timedelta(seconds=15),
                        retry_policy=RetryPolicy(
                            maximum_attempts=3,
                            initial_interval=timedelta(seconds=2),
                            backoff_coefficient=2.0,
                        ),
                    )
                except Exception as e:
                    workflow.logger.warning(f"⚠️ Failed to delete old S3 video: {e}")
            
            # Execute all deletions in parallel
            delete_tasks = [delete_old_video(r) for r in videos_to_replace]
            await asyncio.gather(*delete_tasks)
        
        # =========================================================================
        # Step 7: Upload videos to S3 IN PARALLEL (already validated and hashed)
        # =========================================================================
        all_uploads = videos_to_upload + [r["new_video"] for r in videos_to_replace]
        workflow.logger.info(f"☁️ Uploading {len(all_uploads)} videos to S3 in parallel...")
        
        async def upload_video(idx: int, video_info: dict):
            try:
                result = await workflow.execute_activity(
                    download_activities.upload_single_video,
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
                        video_info.get("duplicate_count", 1),
                        "",  # assister_name
                        "",  # opponent_team
                        video_info.get("source_url", ""),
                        video_info.get("width", 0),
                        video_info.get("height", 0),
                        video_info.get("bitrate", 0.0),
                        video_info.get("file_size", 0),
                    ],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=1.5,
                    ),
                )
                return {"idx": idx, "result": result, "video_info": video_info}
            except Exception as e:
                workflow.logger.warning(f"⚠️ Upload {idx} failed: {str(e)[:100]}")
                return {"idx": idx, "result": None, "error": str(e)}
        
        # Execute all uploads in parallel
        upload_tasks = [upload_video(idx, video_info) for idx, video_info in enumerate(all_uploads)]
        upload_outcomes = await asyncio.gather(*upload_tasks)
        
        video_objects = []  # New schema: list of video objects for MongoDB
        successful_video_urls = []
        
        for outcome in upload_outcomes:
            result = outcome.get("result")
            if result and result.get("status") == "success":
                video_info = outcome["video_info"]
                # Collect video object for MongoDB
                video_objects.append(result.get("video_object", {
                    "url": result["s3_url"],
                    "perceptual_hash": result.get("perceptual_hash", ""),
                    "resolution_score": result.get("resolution_score", 0),
                    "file_size": 0,
                    "popularity": result.get("popularity", 1),
                    "rank": 0,
                }))
                source_url = video_info.get("source_url")
                if source_url:
                    successful_video_urls.append({
                        "video_page_url": source_url,
                        "tweet_url": source_url
                    })
        
        workflow.logger.info(f"☁️ Uploaded {len(video_objects)}/{len(all_uploads)} videos to S3")
        
        # URLs already saved by TwitterWorkflow - no need to save again
        
        # =========================================================================
        # Step 6: Save video objects and cleanup temp directory
        # This saves to _s3_videos array and recalculates ranks
        # =========================================================================
        await workflow.execute_activity(
            download_activities.mark_download_complete,
            args=[fixture_id, event_id, video_objects, temp_dir],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=1),
                backoff_coefficient=2.0,
            ),
        )
        
        # =========================================================================
        # Step 8: Notify frontend to refresh (SSE broadcast)
        # Only notify if we actually uploaded new videos
        # =========================================================================
        if video_objects:
            await workflow.execute_activity(
                monitor_activities.notify_frontend_refresh,
                start_to_close_timeout=timedelta(seconds=10),
                retry_policy=RetryPolicy(maximum_attempts=1),  # Don't retry - frontend may not be running
            )
        
        s3_urls = [v["url"] for v in video_objects]
        workflow.logger.info(f"✅ Download complete: {len(video_objects)} videos in S3")
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": len(video_objects),
            "s3_urls": s3_urls,
        }
