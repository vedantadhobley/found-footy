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


def search_twitter_logic(fixture_id: int, event_id: str, context=None) -> Dict[str, Any]:
    """
    Core Twitter search logic that can be called with or without Dagster context.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID like "5000_234_Goal_1"
        context: Optional OpExecutionContext for logging
        
    Returns:
        Dict with status, event_id, and video_count
    """
    def log(msg: str, level: str = "info"):
        """Helper to log with optional context"""
        if context:
            getattr(context.log, level)(msg)
        else:
            print(f"[{level.upper()}] {msg}")
    
    store = FootyMongoStore()
    
    # Get fixture from active
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        log(f"❌ Fixture {fixture_id} not found", "error")
        return {"status": "error", "event_id": event_id, "video_count": 0}
    
    # Find event in events array
    event = None
    for evt in fixture.get("events", []):
        if evt.get("_event_id") == event_id:
            event = evt
            break
    
    if not event:
        log(f"❌ Event {event_id} not found in fixture {fixture_id}", "error")
        return {"status": "error", "event_id": event_id, "video_count": 0}
    
    # Get prebuilt search string
    twitter_search = event.get("_twitter_search", "Unknown Goal")
    log(f"🐦 Searching Twitter for: {twitter_search}")
    
    # Mark twitter started
    store.mark_event_twitter_started(fixture_id, event_id)
    
    # Search Twitter via session service
    client = TwitterAPIClient()
    discovered_videos = client.search_videos(twitter_search, max_results=5)
    
    log(f"✅ Twitter search complete: {len(discovered_videos)} videos found")
    
    # Mark twitter complete with discovered videos
    store.mark_event_twitter_complete(fixture_id, event_id, discovered_videos)
    
    return {
        "status": "success",
        "event_id": event_id,
        "video_count": len(discovered_videos),
        "videos": discovered_videos
    }


@op(
    name="search_and_save_twitter_videos",
    description="Search Twitter for event videos and save discovered URLs",
    tags={"kind": "twitter", "stage": "discovery"}
)
def search_and_save_twitter_videos_op(
    context: OpExecutionContext, 
    config: TwitterJobConfig
) -> Dict[str, Any]:
    """Op wrapper that calls the logic function with context"""
    return search_twitter_logic(config.fixture_id, config.event_id, context)


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
