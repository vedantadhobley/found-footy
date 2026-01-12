"""
Download Workflow - Video Download/Upload Pipeline

Orchestrates granular download/upload with per-video retry:
1. fetch_event_data - Get existing S3 video metadata for quality comparison
2. download_single_video x N - Download each video individually (3 retries per video)
3. deduplicate_by_md5 - FAST MD5 dedup (eliminates true duplicates before expensive steps)
4. validate_video_is_soccer x N - AI validation (only for MD5-unique videos)
5. generate_video_hash x N - Generate perceptual hash (only for validated videos)
6. deduplicate_videos - Perceptual hash dedup with quality comparison against existing S3
7. replace_s3_video x N - Delete old S3 videos being replaced by higher quality
8. upload_single_video x N - Upload new/replacement videos to S3
9. mark_download_complete - Update MongoDB, cleanup temp dir

Design Philosophy:
- TWO-PHASE DEDUP: MD5 first (fast, exact matches), then perceptual (slow, similar content)
- Per-video retry (3 attempts with exponential backoff)
- Videos that fail all 3 attempts are logged but don't block workflow
- Parallel processing where possible (downloads, hashes, uploads)
- Comprehensive logging at every step with [DOWNLOAD] prefix
- Activity-level heartbeats for long operations (hash generation)

Pipeline Order:
- MD5 dedup happens BEFORE AI validation (saves expensive AI calls for duplicates)
- AI validation happens BEFORE perceptual hash generation
- This saves expensive hash computation for non-soccer/duplicate videos
- Hash generation uses heartbeats (sends heartbeat every 10 frames)

Started by: TwitterWorkflow (WAITS for completion - data integrity)
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
            "md5_deduped": 0,
            "md5_s3_matched": 0,
            "ai_rejected": 0,
            "ai_validation_failed": 0,
            "hash_generated": 0,
            "hash_failed": 0,
            "perceptual_deduped": 0,
            "s3_replaced": 0,
            "s3_popularity_bumped": 0,
            "uploaded": 0,
        }
        
        # =========================================================================
        # Step 1: Get existing S3 video metadata for quality comparison
        # Also gets cross-event dedup data (URLs and hashes from other goals in fixture)
        # =========================================================================
        workflow.logger.info(f"📋 [DOWNLOAD] Fetching existing S3 metadata | event={event_id}")
        
        other_events_hashes = []  # Perceptual hashes from other goals in same fixture
        
        try:
            event_data = await workflow.execute_activity(
                download_activities.fetch_event_data,
                args=[fixture_id, event_id],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            existing_s3_videos = event_data.get("existing_s3_videos", [])
            other_events_hashes = event_data.get("other_events_hashes", [])
            workflow.logger.info(
                f"✅ [DOWNLOAD] Got event data | event={event_id} | "
                f"existing_s3_videos={len(existing_s3_videos)} | "
                f"other_events_hashes={len(other_events_hashes)}"
            )
        except Exception as e:
            workflow.logger.error(f"❌ [DOWNLOAD] fetch_event_data FAILED | event={event_id} | error={e}")
            existing_s3_videos = []
        
        if not discovered_videos:
            workflow.logger.warning(f"⚠️ [DOWNLOAD] No videos to download | event={event_id}")
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        workflow.logger.info(
            f"📋 [DOWNLOAD] Processing {len(discovered_videos)} videos | "
            f"existing_s3={len(existing_s3_videos)} | event={event_id}"
        )
        
        # Temp directory path with unique run ID to prevent conflicts between concurrent workflows
        run_id = workflow.info().run_id[:8]
        temp_dir = f"/tmp/footy_{event_id}_{run_id}"
        
        # =========================================================================
        # Step 2: Download videos IN PARALLEL (with per-video retry)
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
        # Step 3: MD5 Dedup FIRST - Fast elimination of true duplicates
        # This saves AI validation and perceptual hash compute for identical files
        # =========================================================================
        successful_videos = [r for r in download_results if r.get("status") == "success"]
        
        if successful_videos:
            workflow.logger.info(
                f"🔐 [DOWNLOAD] Running fast MD5 dedup | videos={len(successful_videos)} | event={event_id}"
            )
            
            try:
                md5_result = await workflow.execute_activity(
                    download_activities.deduplicate_by_md5,
                    args=[successful_videos, existing_s3_videos],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(maximum_attempts=2),
                )
                
                # Handle S3 exact matches - bump popularity
                s3_exact_matches = md5_result.get("s3_exact_matches", [])
                if s3_exact_matches:
                    workflow.logger.info(
                        f"📈 [DOWNLOAD] Bumping popularity for {len(s3_exact_matches)} MD5-matched S3 videos | event={event_id}"
                    )
                    for match in s3_exact_matches:
                        try:
                            await workflow.execute_activity(
                                download_activities.bump_video_popularity,
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
                                f"⚠️ [DOWNLOAD] Failed to bump MD5-match popularity | error={e}"
                            )
                
                # Handle S3 replacements - queue for later upload after validation
                md5_s3_replacements = md5_result.get("s3_replacements", [])
                
                # Continue with unique videos only
                successful_videos = md5_result.get("unique_videos", [])
                md5_dupes_removed = md5_result.get("md5_duplicates_removed", 0)
                
                # Update stats
                download_stats["md5_deduped"] = md5_dupes_removed
                download_stats["md5_s3_matched"] = len(s3_exact_matches)
                
                workflow.logger.info(
                    f"✅ [DOWNLOAD] MD5 dedup complete | unique={len(successful_videos)} | "
                    f"batch_dupes={md5_dupes_removed} | s3_matches={len(s3_exact_matches)} | "
                    f"s3_replacements={len(md5_s3_replacements)} | event={event_id}"
                )
            except Exception as e:
                workflow.logger.error(f"❌ [DOWNLOAD] MD5 dedup FAILED | error={e} | event={event_id}")
                md5_s3_replacements = []
        else:
            md5_s3_replacements = []
        
        # Add MD5 S3 replacements to the list - they also need validation
        # Mark them so we know to handle them as replacements later
        for replacement in md5_s3_replacements:
            replacement["new_video"]["_is_md5_replacement"] = True
            replacement["new_video"]["_old_s3_video"] = replacement["old_s3_video"]
            successful_videos.append(replacement["new_video"])
        
        # =========================================================================
        # Step 4: AI Validation (only for MD5-unique videos - saves compute!)
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
        # Step 5: Generate perceptual hashes IN PARALLEL for validated videos
        # Uses heartbeat-based timeout (heartbeat every 10 frames)
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
                    # (multiple videos competing for CPU slows ffmpeg calls)
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
        
        validated_videos = videos_with_hash
        
        # =========================================================================
        # Step 6: Deduplicate with quality comparison (perceptual hash)
        # =========================================================================
        workflow.logger.info(f"🔍 [DOWNLOAD] Deduplicating videos | event={event_id}")
        
        # Separate out MD5 replacements - they bypass perceptual dedup (already matched)
        md5_replacement_videos = []
        perceptual_dedup_videos = []
        for video in validated_videos:
            if video.get("_is_md5_replacement"):
                md5_replacement_videos.append({
                    "new_video": video,
                    "old_s3_video": video.get("_old_s3_video", {}),
                })
            else:
                perceptual_dedup_videos.append(video)
        
        try:
            dedup_result = await workflow.execute_activity(
                download_activities.deduplicate_videos,
                args=[perceptual_dedup_videos, existing_s3_videos, other_events_hashes],
                start_to_close_timeout=timedelta(seconds=120),  # Increased from 60s for cross-event checking
                retry_policy=RetryPolicy(maximum_attempts=3),
            )
        except Exception as e:
            workflow.logger.error(f"❌ [DOWNLOAD] Deduplication FAILED | error={e} | event={event_id}")
            dedup_result = {"videos_to_upload": perceptual_dedup_videos, "videos_to_replace": [], "videos_to_bump_popularity": [], "skipped_urls": [], "cross_event_rejected": 0}
        
        videos_to_upload = dedup_result.get("videos_to_upload", [])
        videos_to_replace = dedup_result.get("videos_to_replace", []) + md5_replacement_videos  # Add MD5 replacements
        videos_to_bump_popularity = dedup_result.get("videos_to_bump_popularity", [])
        skipped_urls = dedup_result.get("skipped_urls", [])
        cross_event_rejected = dedup_result.get("cross_event_rejected", 0)
        
        # Update stats
        download_stats["perceptual_deduped"] = len(skipped_urls)
        download_stats["s3_replaced"] = len(videos_to_replace)
        download_stats["s3_popularity_bumped"] = len(videos_to_bump_popularity)
        download_stats["cross_event_rejected"] = cross_event_rejected
        
        total_to_process = len(videos_to_upload) + len(videos_to_replace)
        cross_event_msg = f" | cross_event_rejected={cross_event_rejected}" if cross_event_rejected > 0 else ""
        workflow.logger.info(
            f"✅ [DOWNLOAD] Dedup complete | new={len(videos_to_upload)} | "
            f"replacements={len(videos_to_replace)} (incl {len(md5_replacement_videos)} md5) | "
            f"skipped={len(skipped_urls)}{cross_event_msg} | event={event_id}"
        )
        
        # URLs to track for dedup (even if we don't upload anything)
        all_processed_urls = skipped_urls + filtered_urls + failed_urls
        
        # =========================================================================
        # Step 6b: Bump popularity for existing videos IN PARALLEL
        # (when we skipped uploading because existing was higher quality)
        # =========================================================================
        if videos_to_bump_popularity:
            workflow.logger.info(
                f"📈 [DOWNLOAD] Bumping popularity for {len(videos_to_bump_popularity)} existing videos | event={event_id}"
            )
            
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
                        start_to_close_timeout=timedelta(seconds=15),
                        retry_policy=RetryPolicy(
                            maximum_attempts=2,
                            initial_interval=timedelta(seconds=1),
                        ),
                    )
                except Exception as e:
                    workflow.logger.warning(
                        f"⚠️ [DOWNLOAD] Failed to bump popularity | error={e} | event={event_id}"
                    )
            
            bump_tasks = [bump_popularity(info) for info in videos_to_bump_popularity]
            await asyncio.gather(*bump_tasks)
        
        if total_to_process == 0:
            workflow.logger.info(
                f"⚠️ [DOWNLOAD] No videos to upload (all duplicates/filtered) | "
                f"processed={len(all_processed_urls)} | event={event_id}"
            )
            
            try:
                await workflow.execute_activity(
                    download_activities.mark_download_complete,
                    args=[fixture_id, event_id, [], temp_dir, download_stats],
                    start_to_close_timeout=timedelta(seconds=30),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,
                    ),
                )
            except Exception as e:
                workflow.logger.error(
                    f"❌ [DOWNLOAD] mark_download_complete FAILED | error={e} | event={event_id}"
                )
            
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        # =========================================================================
        # Step 7: Handle replacements - delete old S3 videos IN PARALLEL
        # =========================================================================
        if videos_to_replace:
            workflow.logger.info(
                f"♻️ [DOWNLOAD] Preparing {len(videos_to_replace)} video replacements (reusing S3 keys) | event={event_id}"
            )
            
            async def remove_old_mongo_entry(replacement: dict):
                """Remove old MongoDB entry only - S3 will be overwritten with same key"""
                old_video = replacement["old_s3_video"]
                try:
                    await workflow.execute_activity(
                        download_activities.replace_s3_video,
                        args=[
                            fixture_id,
                            event_id,
                            old_video.get("s3_url", ""),
                            old_video.get("_s3_key", ""),
                            True,  # skip_s3_delete - we'll overwrite with same key
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
                        f"⚠️ [DOWNLOAD] Failed to remove old MongoDB entry | "
                        f"key={old_video.get('_s3_key')} | error={e} | event={event_id}"
                    )
            
            delete_tasks = [remove_old_mongo_entry(r) for r in videos_to_replace]
            await asyncio.gather(*delete_tasks)
        
        # =========================================================================
        # Step 8: Upload videos to S3 IN PARALLEL
        # For replacements, reuse old S3 key to keep shared URLs stable
        # =========================================================================
        all_uploads = videos_to_upload + [r["new_video"] for r in videos_to_replace]
        workflow.logger.info(
            f"☁️ [DOWNLOAD] Uploading {len(all_uploads)} videos to S3 in parallel | event={event_id}"
        )
        
        async def upload_video(idx: int, video_info: dict):
            try:
                # For replacements, _old_s3_key is set - reuse it to keep URLs stable
                existing_s3_key = video_info.get("_old_s3_key", "")
                
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
                        video_info.get("popularity", 1),  # Popularity from dedup
                        "",  # assister_name
                        "",  # opponent_team
                        video_info.get("source_url", ""),
                        video_info.get("width", 0),
                        video_info.get("height", 0),
                        video_info.get("bitrate", 0.0),
                        video_info.get("file_size", 0),
                        existing_s3_key,  # Reuse for replacements, empty for new uploads
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
                    f"❌ [DOWNLOAD] Upload {idx} FAILED | error={str(e)[:100]} | event={event_id}"
                )
                return {"idx": idx, "result": None, "error": str(e)}
        
        upload_tasks = [upload_video(idx, video_info) for idx, video_info in enumerate(all_uploads)]
        upload_outcomes = await asyncio.gather(*upload_tasks)
        
        video_objects = []
        successful_video_urls = []
        
        for outcome in upload_outcomes:
            result = outcome.get("result")
            if result and result.get("status") == "success":
                video_info = outcome["video_info"]
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
        
        workflow.logger.info(
            f"✅ [DOWNLOAD] Uploads complete | success={len(video_objects)}/{len(all_uploads)} | event={event_id}"
        )
        
        # =========================================================================
        # Step 9: Save video objects and cleanup temp directory
        # =========================================================================
        workflow.logger.info(
            f"💾 [DOWNLOAD] Saving {len(video_objects)} video objects to MongoDB | event={event_id}"
        )
        
        # Update final stats
        download_stats["uploaded"] = len(video_objects)
        
        try:
            await workflow.execute_activity(
                download_activities.mark_download_complete,
                args=[fixture_id, event_id, video_objects, temp_dir, download_stats],
                start_to_close_timeout=timedelta(seconds=30),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=2),
                    backoff_coefficient=2.0,
                ),
            )
            workflow.logger.info(f"✅ [DOWNLOAD] Saved to MongoDB | event={event_id}")
        except Exception as e:
            workflow.logger.error(
                f"❌ [DOWNLOAD] mark_download_complete FAILED | error={e} | event={event_id}"
            )
        
        # =========================================================================
        # Step 9: Notify frontend to refresh (SSE broadcast)
        # =========================================================================
        if video_objects:
            try:
                await workflow.execute_activity(
                    monitor_activities.notify_frontend_refresh,
                    start_to_close_timeout=timedelta(seconds=15),
                    retry_policy=RetryPolicy(maximum_attempts=1),
                )
                workflow.logger.info(f"📡 [DOWNLOAD] Frontend notified | event={event_id}")
            except Exception as e:
                workflow.logger.warning(
                    f"⚠️ [DOWNLOAD] Frontend notification failed (non-critical) | error={e} | event={event_id}"
                )
        
        # =========================================================================
        # Step 10: Increment twitter count and check for completion
        # This solves the race condition: _twitter_complete is only set after
        # ALL downloads finish (not just after all searches start)
        # =========================================================================
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
        
        s3_urls = [v["url"] for v in video_objects]
        
        workflow.logger.info(
            f"🎉 [DOWNLOAD] WORKFLOW COMPLETE | uploaded={len(video_objects)} | "
            f"s3_urls={len(s3_urls)} | event={event_id}"
        )
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": len(video_objects),
            "s3_urls": s3_urls,
        }
