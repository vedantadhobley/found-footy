"""
Twitter Workflow - Per Stable Event

Searches Twitter for videos, then triggers DownloadWorkflow if videos found.
"""
from temporalio import workflow
from datetime import timedelta

with workflow.unsafe.imports_passed_through():
    from src.activities import twitter as twitter_activities
    from src.workflows.download_workflow import DownloadWorkflow


@workflow.defn
class TwitterWorkflow:
    """Search Twitter for event videos"""
    
    @workflow.run
    async def run(self, fixture_id: int, event_id: str) -> dict:
        """
        Workflow:
        1. Get event from fixtures_active
        2. Build search query (team + player + minute)
        3. POST to twitter-session service
        4. Parse video URLs from response
        5. Save discovered_videos to event
        6. Mark twitter_complete
        7. If videos found → trigger DownloadWorkflow
        """
        
        # Search Twitter for videos
        result = await workflow.execute_activity(
            twitter_activities.search_event_videos,
            args=[fixture_id, event_id],
            start_to_close_timeout=timedelta(seconds=120),
        )
        
        # If videos found, trigger download workflow
        video_count = result.get("video_count", 0)
        if video_count > 0:
            workflow_id = f"download-{fixture_id}-{event_id}"
            
            # Start DownloadWorkflow as child workflow
            await workflow.execute_child_workflow(
                DownloadWorkflow.run,
                args=[fixture_id, event_id],
                id=workflow_id,
            )
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_found": video_count,
        }
