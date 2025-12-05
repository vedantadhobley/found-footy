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
        is_retry: bool = False,
        fixture_finished: bool = False,
    ) -> dict:
        """
        Execute the Twitter video discovery pipeline.
        
        Args:
            fixture_id: The fixture ID
            event_id: The event ID (format: {fixture}_{team}_{player}_{type}_{#})
            player_name: Player name for workflow ID naming
            team_name: Team name for workflow ID naming
            is_retry: True if this is a retry after getting <5 videos
            fixture_finished: True if fixture has ended (FT/AET/PEN) - no more retries
        
        Returns:
            Dict with fixture_id, event_id, videos_found count, needs_retry flag
        """
        workflow.logger.info(f"🐦 Starting Twitter search for {event_id}")
        
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
        workflow.logger.info(f"🔍 Search query: '{twitter_search}'")
        
        # =========================================================================
        # Step 2: Execute Twitter search (the risky external call)
        # =========================================================================
        search_result = await workflow.execute_activity(
            twitter_activities.execute_twitter_search,
            args=[twitter_search, 5],  # max 5 videos
            start_to_close_timeout=timedelta(seconds=150),  # 2.5 min (120s search + buffer)
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=10),
                backoff_coefficient=1.5,  # 10s → 15s → 22.5s
            ),
        )
        
        videos = search_result.get("videos", [])
        video_count = len(videos)
        workflow.logger.info(f"📹 Found {video_count} videos")
        
        # =========================================================================
        # Step 3: Decide if we need to retry (wait 1 more cycle)
        # =========================================================================
        # Retry logic: If <5 videos AND fixture not finished AND not already a retry
        needs_retry = video_count < 5 and not fixture_finished and not is_retry
        
        if needs_retry:
            workflow.logger.info(
                f"⏳ Only {video_count} videos found - will retry next cycle "
                f"(fixture still in progress)"
            )
        
        # =========================================================================
        # Step 4: Save results to MongoDB
        # =========================================================================
        # Pass needs_retry so mongo knows whether to mark complete or pending
        await workflow.execute_activity(
            twitter_activities.save_twitter_results,
            args=[fixture_id, event_id, videos, needs_retry],
            start_to_close_timeout=timedelta(seconds=10),
            retry_policy=RetryPolicy(maximum_attempts=2),
        )
        
        workflow.logger.info(f"💾 Saved results for {event_id}")
        
        # =========================================================================
        # Step 5: Trigger DownloadWorkflow if videos found (and not deferring to retry)
        # =========================================================================
        # Only download if we have videos AND (not retrying OR this is the retry OR fixture finished)
        should_download = video_count > 0 and (not needs_retry or is_retry or fixture_finished)
        
        if should_download:
            # Build human-readable workflow ID
            player_last = player_name.split()[-1] if player_name else "Unknown"
            team_clean = team_name.replace(" ", "_").replace(".", "_").replace("-", "_")
            retry_suffix = "-retry" if is_retry else ""
            download_workflow_id = f"download-{team_clean}-{player_last}-{video_count}vids{retry_suffix}-{event_id}"
            
            workflow.logger.info(f"⬇️ Starting download: {download_workflow_id}")
            
            await workflow.execute_child_workflow(
                DownloadWorkflow.run,
                args=[fixture_id, event_id, player_name, team_name],
                id=download_workflow_id,
            )
            
            workflow.logger.info(f"✅ Download complete for {event_id}")
        elif needs_retry:
            workflow.logger.info(f"⏸️ Deferring download - waiting for retry with more videos")
        else:
            workflow.logger.info(f"⚠️ No videos found, skipping download")
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_found": video_count,
            "needs_retry": needs_retry,
            "is_retry": is_retry,
        }
