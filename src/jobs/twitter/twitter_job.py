"""Twitter Job - Discover event videos on Twitter"""
from dagster import Config, job, op, OpExecutionContext
import time
from typing import Dict, Any, List
from src.data.mongo_store import FootyMongoStore


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
    
    # TODO: Integrate actual Twitter search when ready
    # For now, return empty list (placeholder)
    context.log.info("⏳ Twitter search not yet implemented - marking as complete with no videos")
    
    discovered_videos = []
    
    # Mark twitter complete
    store.mark_event_twitter_complete(fixture_id, event_id, discovered_videos)
    
    context.log.info(f"✅ Twitter search complete: {len(discovered_videos)} videos found")
    
    return {
        "status": "success",
        "event_id": event_id,
        "fixture_id": fixture_id,
        "video_count": len(discovered_videos)
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
