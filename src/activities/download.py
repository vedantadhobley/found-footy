"""Download activities"""
from temporalio import activity
from typing import Dict, List, Any


@activity.defn
async def download_and_upload_videos(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Download videos, deduplicate, and upload to S3.
    
    Process:
    1. Fetch event from fixtures_active (has discovered_videos)
    2. Download each video to /tmp with yt-dlp
    3. Calculate MD5 hash for each file
    4. Deduplicate: keep largest file per hash
    5. Upload to S3: fixture_id/event_id/filename
    6. Add metadata tags: player, team, event_id, fixture_id
    7. Mark download_complete with s3_urls
    8. Cleanup /tmp
    """
    # TODO: Implement download logic from src-dagster/jobs/download/download_job.py
    return {
        "uploaded_count": 0,
        "s3_urls": [],
    }
