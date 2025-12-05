"""
Download Workflow - Per Event with Videos

Downloads videos, deduplicates, uploads to S3 with metadata tags.
"""
from temporalio import workflow
from temporalio.common import RetryPolicy
from datetime import timedelta

with workflow.unsafe.imports_passed_through():
    from src.activities import download as download_activities


@workflow.defn
class DownloadWorkflow:
    """Download, deduplicate, and upload videos to S3"""
    
    @workflow.run
    async def run(self, fixture_id: int, event_id: str, player_name: str = "", team_name: str = "") -> dict:
        """
        Workflow:
        1. Fetch event data from fixtures_active
        2. Download videos to /tmp with yt-dlp
        3. Calculate MD5 hashes for deduplication
        4. Keep largest file per duplicate hash
        5. Upload to S3 (fixture_id/event_id/)
        6. Add metadata tags (player, team, event_id)
        7. Mark download_complete with s3_urls
        8. Cleanup /tmp
        """
        
        # Download and process videos
        result = await workflow.execute_activity(
            download_activities.download_and_upload_videos,
            args=[fixture_id, event_id],
            start_to_close_timeout=timedelta(minutes=10),
            retry_policy=RetryPolicy(
                maximum_attempts=3,
                initial_interval=timedelta(seconds=10),
            ),
        )
        
        return {
            "fixture_id": fixture_id,
            "event_id": event_id,
            "videos_uploaded": result.get("uploaded_count", 0),
            "s3_urls": result.get("s3_urls", []),
        }
