"""
Download Workflow - Video Download/Upload Pipeline

Orchestrates granular download/upload with per-video retry:
1. fetch_event_data - Get discovered_videos and existing S3 video metadata
2. download_single_video x N - Download each video individually
3. deduplicate_videos - Hash dedup with quality comparison against existing S3
4. replace_s3_video x N - Delete old S3 videos being replaced by higher quality
5. upload_single_video x N - Upload new/replacement videos to S3
6. save_processed_urls - Track all processed URLs for future dedup
7. mark_download_complete - Update MongoDB, cleanup temp dir

Per-video retry ensures:
- Failed download on video 3/5 doesn't lose progress on 1-2
- Failed upload on video 4/5 doesn't re-download everything
- Partial success is preserved (3/5 videos uploaded = 3 videos in S3)

Cross-retry quality comparison:
- New videos that match existing S3 by perceptual hash are compared by quality
- Higher quality new videos REPLACE existing S3 videos (resolution > bitrate > file_size)
- Lower quality matches are SKIPPED but tracked for dedup
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta

with workflow.unsafe.imports_passed_through():
    from src.activities import download as download_activities


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
        
        # Temp directory path (created by first download activity)
        temp_dir = f"/tmp/footy_{event_id}"
        
        # =========================================================================
        # Step 2: Download each video individually (with per-video retry)
        # 403 errors are common (rate limits, expired links) - retry 3x with backoff
        # Also track filtered URLs (too short/long) for dedup
        # =========================================================================
        workflow.logger.info(f"📥 Downloading {len(discovered_videos)} videos...")
        
        download_results = []
        filtered_urls = []  # Track URLs we filtered out (too short/long)
        
        for idx, video in enumerate(discovered_videos):
            video_url = video.get("tweet_url") or video.get("video_page_url")
            if not video_url:
                workflow.logger.warning(f"⚠️ Video {idx}: No URL, skipping")
                continue
            
            try:
                result = await workflow.execute_activity(
                    download_activities.download_single_video,
                    args=[video_url, idx, event_id, temp_dir, video_url],
                    start_to_close_timeout=timedelta(seconds=60),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=2),
                        backoff_coefficient=2.0,  # 2s, 4s, 8s
                        maximum_interval=timedelta(seconds=10),
                    ),
                )
                download_results.append(result)
                
                # Track filtered videos (too short/long) for dedup
                if result.get("status") == "filtered":
                    source_url = result.get("source_url")
                    if source_url:
                        filtered_urls.append(source_url)
                        workflow.logger.info(f"📝 Tracking filtered URL for dedup: {result.get('reason')}")
                        
            except Exception as e:
                workflow.logger.warning(f"⚠️ Video {idx} failed after 3 retries: {str(e)[:100]}")
                download_results.append({"status": "failed", "error": str(e)[:200]})
        
        successful_downloads = sum(1 for r in download_results if r.get("status") == "success")
        filtered_count = len(filtered_urls)
        workflow.logger.info(f"📥 Downloaded {successful_downloads}/{len(discovered_videos)} videos ({filtered_count} filtered)")

        
        # =========================================================================
        # Step 3: Deduplicate with quality comparison
        # Returns: videos_to_upload, videos_to_replace, skipped_urls
        # =========================================================================
        dedup_result = await workflow.execute_activity(
            download_activities.deduplicate_videos,
            args=[download_results, existing_s3_videos],
            start_to_close_timeout=timedelta(seconds=30),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        videos_to_upload = dedup_result.get("videos_to_upload", [])
        videos_to_replace = dedup_result.get("videos_to_replace", [])
        skipped_urls = dedup_result.get("skipped_urls", [])
        
        total_to_process = len(videos_to_upload) + len(videos_to_replace)
        
        if total_to_process == 0 and not skipped_urls and not filtered_urls:
            workflow.logger.warning(f"⚠️ No videos to upload (all downloads failed)")
            await workflow.execute_activity(
                download_activities.mark_download_complete,
                args=[fixture_id, event_id, [], [], [], temp_dir],
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
        
        # Even if no videos to upload, save filtered URLs for dedup
        if total_to_process == 0:
            workflow.logger.info(f"📝 No videos to upload, but saving {len(skipped_urls) + len(filtered_urls)} URLs for dedup")
            all_processed_urls = skipped_urls + filtered_urls
            if all_processed_urls:
                await workflow.execute_activity(
                    download_activities.save_processed_urls,
                    args=[fixture_id, event_id, all_processed_urls],
                    start_to_close_timeout=timedelta(seconds=10),
                    retry_policy=RetryPolicy(
                        maximum_attempts=3,
                        initial_interval=timedelta(seconds=1),
                        backoff_coefficient=2.0,
                    ),
                )
            await workflow.execute_activity(
                download_activities.mark_download_complete,
                args=[fixture_id, event_id, [], [], [], temp_dir],
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
        # Step 4: Handle replacements - delete old S3 videos first
        # =========================================================================
        for replacement in videos_to_replace:
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
                        old_video.get("perceptual_hash", ""),
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
                # Continue anyway - we'll still upload the new one
        
        # =========================================================================
        # Step 5: Upload new videos and replacements
        # =========================================================================
        # Combine new uploads and replacement uploads
        all_uploads = videos_to_upload + [r["new_video"] for r in videos_to_replace]
        
        workflow.logger.info(f"☁️ Uploading {len(all_uploads)} videos to S3...")
        
        s3_urls = []
        perceptual_hashes = []
        successful_video_urls = []
        
        for idx, video_info in enumerate(all_uploads):
            if "perceptual_hash" in video_info:
                perceptual_hashes.append(video_info["perceptual_hash"])
            
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
                
                if result.get("status") == "success":
                    s3_urls.append(result["s3_url"])
                    source_url = video_info.get("source_url")
                    if source_url:
                        successful_video_urls.append({
                            "video_page_url": source_url,
                            "tweet_url": source_url
                        })
            except Exception as e:
                workflow.logger.warning(f"⚠️ Upload {idx} failed: {str(e)[:100]}")
        
        workflow.logger.info(f"☁️ Uploaded {len(s3_urls)}/{len(all_uploads)} videos to S3")
        
        # =========================================================================
        # Step 6: Save ALL processed URLs for dedup tracking
        # Combine: skipped (existing S3 better) + filtered (too short/long) + successful uploads
        # This ensures future Twitter searches don't re-discover these URLs
        # =========================================================================
        all_processed_urls = []
        
        # Add skipped URLs (we downloaded but existing S3 was better quality)
        all_processed_urls.extend(skipped_urls)
        
        # Add filtered URLs (too short/long - collected during download loop)
        all_processed_urls.extend(filtered_urls)
        
        # Add successful upload URLs
        for video_info in all_uploads:
            source_url = video_info.get("source_url")
            if source_url:
                all_processed_urls.append(source_url)
        
        if all_processed_urls:
            workflow.logger.info(f"📝 Saving {len(all_processed_urls)} processed URLs for dedup")
            await workflow.execute_activity(
                download_activities.save_processed_urls,
                args=[fixture_id, event_id, all_processed_urls],
                start_to_close_timeout=timedelta(seconds=10),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=1),
                    backoff_coefficient=2.0,
                ),
            )
        
        # =========================================================================
        # Step 7: Save S3 results and cleanup temp directory
        # Note: URLs are saved separately above, this just saves s3_urls and hashes
        # =========================================================================
        await workflow.execute_activity(
            download_activities.mark_download_complete,
            args=[fixture_id, event_id, s3_urls, perceptual_hashes, [], temp_dir],  # Empty list for URLs - saved above
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=1),
                backoff_coefficient=2.0,
            ),
        )
        
        workflow.logger.info(f"✅ Download complete: {len(s3_urls)} videos in S3")
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": len(s3_urls),
            "s3_urls": s3_urls,
        }
