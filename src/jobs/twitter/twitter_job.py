"""Twitter Job - Discover event videos on Twitter"""
from dagster import Config, job, op, OpExecutionContext
import os
import requests
from typing import Dict, Any, List
from src.data.mongo_store import FootyMongoStore


class TwitterAPIClient:
    """Simple Twitter session client"""
    
    def __init__(self):
        self.session_url = os.getenv('TWITTER_SESSION_URL', 'http://twitter-session:8888')
        
    def search_videos(self, search_query: str, max_results: int = 5) -> List[Dict[str, Any]]:
        """Search videos via session service - fail if unavailable"""
        try:
            response = requests.post(
                f"{self.session_url}/search",
                json={"search_query": search_query, "max_results": max_results},
                timeout=60
            )
            
            if response.status_code == 200:
                data = response.json()
                videos = data.get("videos", [])
                return videos
            else:
                return []
                
        except requests.exceptions.ConnectionError:
            return []
        except Exception:
            return []


class TwitterJobConfig(Config):
    """Configuration for twitter job"""
    fixture_id: int
    event_id: str


@op(
    name="search_and_save_twitter_videos",
    description="Search Twitter for event videos and save discovered URLs",
    tags={"kind": "twitter", "stage": "discovery"}
)
def search_and_save_twitter_videos_op(
    context: OpExecutionContext, 
    config: TwitterJobConfig
) -> Dict[str, Any]:
    """
    Search Twitter for videos and save discovered video URLs to event in fixtures_active.
    
    Reads event from fixtures_active.events array (already has _twitter_search field).
    Marks _twitter_complete when done.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID like "5000_234_Goal_1"
        
    Returns:
        Dict with event_id and discovered video count
    """
    fixture_id = config.fixture_id
    event_id = config.event_id
    
    store = FootyMongoStore()
    
    # Get fixture from active
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        context.log.error(f"❌ Fixture {fixture_id} not found")
        return {"status": "error", "event_id": event_id}
    
    # Find event in events array
    event = None
    for evt in fixture.get("events", []):
        if evt.get("_event_id") == event_id:
            event = evt
            break
    
    if not event:
        context.log.error(f"❌ Event {event_id} not found in fixture {fixture_id}")
        return {"status": "error", "event_id": event_id}
    
    # Get prebuilt search string
    twitter_search = event.get("_twitter_search", "Unknown Goal")
    context.log.info(f"🐦 Searching Twitter for: {twitter_search}")
    
    # Mark twitter started
    store.mark_event_twitter_started(fixture_id, event_id)
    
    # Search Twitter via session service
    client = TwitterAPIClient()
    discovered_videos = client.search_videos(twitter_search, max_results=5)
    
    context.log.info(f"✅ Twitter search complete: {len(discovered_videos)} videos found")
    
    # Mark twitter complete with discovered videos
    store.mark_event_twitter_complete(fixture_id, event_id, discovered_videos)
    
    # Trigger download job if videos found
    if discovered_videos:
        context.log.info(f"📥 Found {len(discovered_videos)} videos - triggering download job")
        
        from src.jobs.download.download_job import (
            fetch_event_videos_op,
            download_and_deduplicate_op,
            upload_to_s3_with_tags_op,
            mark_download_complete_op,
            DownloadJobConfig
        )
        try:
            download_config = DownloadJobConfig(fixture_id=fixture_id, event_id=event_id)
            
            # Execute download pipeline
            fetch_result = fetch_event_videos_op(context, download_config)
            if fetch_result.get("status") == "success":
                download_result = download_and_deduplicate_op(context, fetch_result)
                if download_result.get("status") == "success":
                    upload_result = upload_to_s3_with_tags_op(context, download_result)
                    if upload_result.get("status") == "success":
                        mark_download_complete_op(context, upload_result)
                        context.log.info(f"✅ Download pipeline completed for {event_id}")
        except Exception as e:
            context.log.error(f"❌ Error executing download pipeline: {e}")
    
    return {
        "status": "success",
        "event_id": event_id,
        "fixture_id": fixture_id,
        "video_count": len(discovered_videos),
        "discovered_videos": discovered_videos
    }


@job(
    name="twitter_job",
    description="Search Twitter and discover video URLs for an event",
    tags={"pipeline": "twitter", "trigger": "sensor", "phase": "discovery"}
)
def twitter_job():
    """
    Search Twitter for event videos.
    
    Called PER EVENT after debounce_job marks _debounce_complete=true.
    
    Flow:
    1. Get event from fixtures_active.events array
    2. Use prebuilt _twitter_search field
    3. Search Twitter for videos
    4. Mark _twitter_complete and save discovered video URLs
    
    All updates happen in-place in fixtures_active.events array.
    
    Config (fixture_id, event_id) provided at runtime via RunConfig.
    """
    search_and_save_twitter_videos_op()
