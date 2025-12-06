"""
Download Workflow - Video Download/Upload Pipeline

Orchestrates granular download/upload with per-video retry:
1. fetch_event_data - Get discovered_videos from MongoDB (quick)
2. download_single_video x N - Download each video individually (3 retries each)
3. deduplicate_videos - MD5 hash dedup, keep largest per hash
4. upload_single_video x N - Upload each unique video to S3 (3 retries each)
5. mark_download_complete - Update MongoDB, cleanup temp dir

Per-video retry ensures:
- Failed download on video 3/5 doesn't lose progress on 1-2
- Failed upload on video 4/5 doesn't re-download everything
- Partial success is preserved (3/5 videos uploaded = 3 videos in S3)
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
    
    Uses 5 granular activities for proper retry semantics:
    - Each video download/upload is independent
    - Failures don't cascade (partial success preserved)
    """
    
    @workflow.run
    async def run(
        self,
        fixture_id: int,
        event_id: str,
        player_name: str = "",
        team_name: str = "",
    ) -> dict:
        """
        Execute the video download pipeline.
        
        Args:
            fixture_id: The fixture ID
            event_id: The event ID
            player_name: Player name for S3 metadata
            team_name: Team name for S3 metadata
        
        Returns:
            Dict with videos_uploaded count and s3_urls list
        """
        workflow.logger.info(f"⬇️ Starting download for {event_id}")
        
        # =========================================================================
        # Step 1: Fetch event data (discovered videos from Twitter)
        # =========================================================================
        event_data = await workflow.execute_activity(
            download_activities.fetch_event_data,
            args=[fixture_id, event_id],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        if event_data.get("status") != "success":
            workflow.logger.warning(f"⚠️ No videos to download")
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        discovered_videos = event_data["discovered_videos"]
        player_name = event_data["player_name"]
        team_name = event_data["team_name"]
        
        workflow.logger.info(f"📋 Found {len(discovered_videos)} videos to download")
        
        # Temp directory path (created by first download activity)
        temp_dir = f"/tmp/footy_{event_id}"
        
        # =========================================================================
        # Step 2: Download each video individually (with per-video retry)
        # =========================================================================
        workflow.logger.info(f"📥 Downloading {len(discovered_videos)} videos...")
        
        download_results = []
        for idx, video in enumerate(discovered_videos):
            video_url = video.get("tweet_url") or video.get("video_page_url")
            if not video_url:
                workflow.logger.warning(f"⚠️ Video {idx}: No URL, skipping")
                continue
            
            result = await workflow.execute_activity(
                download_activities.download_single_video,
                args=[video_url, idx, event_id, temp_dir],
                start_to_close_timeout=timedelta(minutes=2),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=5),
                    backoff_coefficient=1.5,  # 5s → 7.5s → 11s
                ),
            )
            download_results.append(result)
        
        successful_downloads = sum(1 for r in download_results if r.get("status") == "success")
        workflow.logger.info(f"📥 Downloaded {successful_downloads}/{len(discovered_videos)} videos")
        
        # =========================================================================
        # Step 3: Deduplicate by MD5 hash (keep largest per hash)
        # =========================================================================
        unique_videos = await workflow.execute_activity(
            download_activities.deduplicate_videos,
            args=[download_results],
            start_to_close_timeout=timedelta(seconds=30),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        if not unique_videos:
            workflow.logger.warning(f"⚠️ No videos to upload (all downloads failed)")
            # Cleanup and mark complete with empty list
            await workflow.execute_activity(
                download_activities.mark_download_complete,
                args=[fixture_id, event_id, [], temp_dir],
                start_to_close_timeout=timedelta(seconds=10),
            )
            return {
                "fixture_id": fixture_id,
                "event_id": event_id,
                "videos_uploaded": 0,
                "s3_urls": [],
            }
        
        workflow.logger.info(f"🔄 {len(unique_videos)} unique videos after deduplication")
        
        # =========================================================================
        # Step 4: Upload each unique video individually (with per-video retry)
        # =========================================================================
        workflow.logger.info(f"☁️ Uploading {len(unique_videos)} videos to S3...")
        
        s3_urls = []
        for idx, video_info in enumerate(unique_videos):
            result = await workflow.execute_activity(
                download_activities.upload_single_video,
                args=[
                    video_info["file_path"],
                    fixture_id,
                    event_id,
                    player_name,
                    team_name,
                    idx,
                ],
                start_to_close_timeout=timedelta(minutes=2),
                retry_policy=RetryPolicy(
                    maximum_attempts=3,
                    initial_interval=timedelta(seconds=5),
                    backoff_coefficient=1.5,
                ),
            )
            
            if result.get("status") == "success":
                s3_urls.append(result["s3_url"])
        
        workflow.logger.info(f"☁️ Uploaded {len(s3_urls)}/{len(unique_videos)} videos to S3")
        
        # =========================================================================
        # Step 5: Mark complete and cleanup temp directory
        # =========================================================================
        await workflow.execute_activity(
            download_activities.mark_download_complete,
            args=[fixture_id, event_id, s3_urls, temp_dir],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        workflow.logger.info(f"✅ Download complete: {len(s3_urls)} videos in S3")
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": len(s3_urls),
            "s3_urls": s3_urls,
        }
