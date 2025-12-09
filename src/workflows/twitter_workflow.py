"""
Twitter Workflow - Video Discovery Pipeline

Orchestrates the search for goal videos on Twitter using 3 granular activities:
1. get_twitter_search_data - Get search query from MongoDB (fast, rarely fails)
2. execute_twitter_search - POST to Firefox automation (slow, needs retry)
3. save_twitter_results - Save videos to MongoDB (fast, rarely fails)

Triggered by MonitorWorkflow when an event reaches _stable_count >= 3.
If videos are found, triggers DownloadWorkflow as a child workflow.

Retry Strategy:
- Activity 1 (get data): 2 attempts, 1s interval
- Activity 2 (search): 3 attempts, 10s interval (browser automation is slow)
- Activity 3 (save): 2 attempts, 1s interval
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta
from typing import Optional

with workflow.unsafe.imports_passed_through():
    from src.activities import twitter as twitter_activities
    from src.workflows.download_workflow import DownloadWorkflow


@workflow.defn
class TwitterWorkflow:
    """
    Search Twitter for event videos and trigger download if found.
    
    Uses 3 separate activities for granular retry control:
    - If search fails, only the search is retried (not the MongoDB reads)
    - If save fails, the videos are preserved in workflow state
    """
    
    @workflow.run
    async def run(
        self,
        fixture_id: int,
        event_id: str,
        player_name: str = "",
        team_name: str = "",
        is_retry: int = 0,
        fixture_finished: bool = False,
    ) -> dict:
        """
        Execute the Twitter video discovery pipeline (single attempt per invocation).
        
        Monitor workflow triggers this multiple times (up to 3 attempts with time gaps).
        Each attempt deduplicates URLs and hashes against previous attempts.
        
        Args:
            fixture_id: The fixture ID
            event_id: The event ID (format: {fixture}_{team}_{player}_{type}_{#})
            player_name: Player name for workflow ID naming
            team_name: Team name for workflow ID naming
            is_retry: Attempt number (1, 2, 3) - kept as is_retry for backwards compatibility
            fixture_finished: Whether fixture is finished (FT/AET/PEN)
        
        Returns:
            Dict with videos_discovered, videos_uploaded for this attempt
        """
        attempt_number = is_retry if is_retry > 0 else 1
        workflow.logger.info(f"🐦 Starting Twitter search for {event_id} (attempt {attempt_number}/3)")
        
        # =========================================================================
        # Step 1: Get search query from MongoDB
        # =========================================================================
        search_data = await workflow.execute_activity(
            twitter_activities.get_twitter_search_data,
            args=[fixture_id, event_id],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        twitter_search = search_data["twitter_search"]
        existing_video_urls = search_data.get("existing_video_urls", [])
        workflow.logger.info(f"🔍 Search query: '{twitter_search}' ({len(existing_video_urls)} existing URLs)")
        
        # =========================================================================
        # Step 2: Execute Twitter search (the risky external call)
        # =========================================================================
        search_result = await workflow.execute_activity(
            twitter_activities.execute_twitter_search,
            args=[twitter_search, 3, existing_video_urls],
            start_to_close_timeout=timedelta(seconds=150),
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=10),
                backoff_coefficient=1.5,
            ),
        )
        
        videos = search_result.get("videos", [])
        video_count = len(videos)
        workflow.logger.info(f"📹 Found {video_count} videos")
        
        # =========================================================================
        # Step 3: Save discovered videos (append to _discovered_videos array)
        # =========================================================================
        await workflow.execute_activity(
            twitter_activities.save_discovered_videos,
            args=[fixture_id, event_id, videos],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        workflow.logger.info(f"💾 Saved {video_count} discovered videos for {event_id}")
        
        # =========================================================================
        # Step 4: Always trigger DownloadWorkflow (even if 0 new videos)
        # =========================================================================
        player_last = player_name.split()[-1] if player_name else "Unknown"
        team_clean = team_name.replace(" ", "_").replace(".", "_").replace("-", "_")
        
        # Use attempt number in workflow ID (default to 1 for backwards compatibility)
        attempt_number = is_retry if is_retry > 0 else 1
        download_workflow_id = f"download{attempt_number}-{team_clean}-{player_last}-{video_count}vids-{event_id}"
        
        workflow.logger.info(f"⬇️ Starting download: {download_workflow_id}")
        
        s3_count = 0
        try:
            download_result = await workflow.execute_child_workflow(
                DownloadWorkflow.run,
                args=[fixture_id, event_id, player_name, team_name],
                id=download_workflow_id,
                execution_timeout=timedelta(minutes=15),
            )
            
            s3_count = download_result.get("videos_uploaded", 0)
            workflow.logger.info(f"✅ Download complete: {s3_count} videos in S3 for {event_id}")
        except Exception as e:
            workflow.logger.error(f"❌ Download workflow failed: {e}")
            workflow.logger.info(f"⚠️ Continuing despite download failure - videos remain in MongoDB")
        
        # =========================================================================
        # Step 5: Return results
        # =========================================================================
        workflow.logger.info(f"✅ Twitter search complete: {video_count} videos found, {s3_count} in S3")
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_discovered": video_count,
            "videos_uploaded": s3_count,
            "attempt_number": attempt_number,
        }
