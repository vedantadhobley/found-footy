"""
Twitter Activities - Granular video discovery pipeline

Three-activity pattern for Twitter search with proper retry semantics:
1. get_twitter_search_data - Fetch event and search query from MongoDB
2. execute_twitter_search - POST to Firefox automation (the risky call)
3. save_twitter_results - Persist results back to MongoDB

This separation ensures:
- Twitter search failures trigger retry (not swallowed)
- Successful searches don't get lost if save fails
- Clear visibility in Temporal UI of which step failed
"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import requests


# =============================================================================
# Activity 1: Get Twitter Search Data
# =============================================================================

@activity.defn
async def get_twitter_search_data(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Fetch event data and return the Twitter search query.
    
    This is a quick MongoDB read that should rarely fail.
    Also marks _twitter_started=true for tracking.
    
    Args:
        fixture_id: The fixture ID
        event_id: The event ID to search for
    
    Returns:
        Dict with twitter_search query and event metadata
    
    Raises:
        ValueError: If fixture or event not found (unrecoverable)
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Fetch fixture from active collection
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        msg = f"Fixture {fixture_id} not found in fixtures_active"
        activity.logger.error(f"❌ {msg}")
        raise ValueError(msg)
    
    # Find the specific event
    event = None
    for evt in fixture.get("events", []):
        if evt.get("_event_id") == event_id:
            event = evt
            break
    
    if not event:
        msg = f"Event {event_id} not found in fixture {fixture_id}"
        activity.logger.error(f"❌ {msg}")
        raise ValueError(msg)
    
    # Get prebuilt search string (set by process_fixture_events)
    twitter_search = event.get("_twitter_search", "")
    if not twitter_search:
        msg = f"No _twitter_search field on event {event_id}"
        activity.logger.error(f"❌ {msg}")
        raise ValueError(msg)
    
    # Get existing discovered videos (for retry deduplication)
    existing_videos = event.get("_discovered_videos", [])
    # Twitter service uses video_page_url field
    existing_urls = [v.get("video_page_url") or v.get("url") for v in existing_videos if v.get("video_page_url") or v.get("url")]
    
    # Mark search as started (for debugging/monitoring)
    store.mark_event_twitter_started(fixture_id, event_id)
    
    if existing_urls:
        activity.logger.info(f"🔍 Got search query: '{twitter_search}' for {event_id} (will skip {len(existing_urls)} existing videos)")
    else:
        activity.logger.info(f"🔍 Got search query: '{twitter_search}' for {event_id}")
    
    return {
        "twitter_search": twitter_search,
        "fixture_id": fixture_id,
        "event_id": event_id,
        "existing_video_urls": existing_urls,
    }


# =============================================================================
# Activity 2: Execute Twitter Search (The Risky External Call)
# =============================================================================

@activity.defn
async def execute_twitter_search(
    twitter_search: str, 
    max_results: int = 3,
    existing_video_urls: Optional[List[str]] = None
) -> Dict[str, Any]:
    """
    POST to Twitter browser automation service.
    
    This is the risky external call that needs proper retry policy.
    If this fails, Temporal will retry the activity (not swallow the error).
    
    On retry attempts, filters out videos we've already discovered to avoid
    re-downloading duplicates.
    
    Args:
        twitter_search: Search query (e.g., "Salah Liverpool")
        max_results: Max videos to return (default 3)
        existing_video_urls: List of video URLs already discovered (for retry deduplication)
    
    Returns:
        Dict with videos array (filtered to exclude existing URLs)
    
    Raises:
        ConnectionError: Twitter service unavailable
        TimeoutError: Search took too long
        RuntimeError: Non-200 response from service (including 503 auth required)
    """
    session_url = os.getenv("TWITTER_SESSION_URL", "http://twitter:8888")
    
    activity.logger.info(f"🐦 Searching Twitter: '{twitter_search}'")
    activity.logger.info(f"📡 POST {session_url}/search")
    
    try:
        response = requests.post(
            f"{session_url}/search",
            json={"search_query": twitter_search, "max_results": max_results},
            timeout=120,  # 2 min for browser automation
        )
    except requests.exceptions.ConnectionError as e:
        activity.logger.error(f"❌ Twitter service unavailable")
        raise ConnectionError(f"Twitter service at {session_url} unreachable: {e}")
    except requests.exceptions.Timeout:
        activity.logger.error(f"❌ Search timed out after 120s")
        raise TimeoutError(f"Twitter search timed out for '{twitter_search}'")
    
    # Check for 503 - this means authentication is required
    # This is a FAILURE that should trigger retry (after manual login)
    if response.status_code == 503:
        try:
            error_data = response.json()
            error_msg = error_data.get("detail", {}).get("message", "Authentication required")
        except:
            error_msg = "Twitter authentication required - manual login needed"
        
        activity.logger.error(f"❌ TWITTER AUTH FAILED: {error_msg}")
        activity.logger.error(f"   🔐 Open VNC at http://localhost:4103 to login")
        raise RuntimeError(f"Twitter authentication required: {error_msg}")
    
    # Check other error responses
    if response.status_code != 200:
        activity.logger.error(f"❌ Service returned {response.status_code}")
        raise RuntimeError(f"Twitter service error {response.status_code}: {response.text}")
    
    # Parse videos from response
    data = response.json()
    videos = data.get("videos", [])
    
    # Filter out videos we've already discovered (on retry)
    # Twitter service uses video_page_url field
    if existing_video_urls:
        original_count = len(videos)
        videos = [v for v in videos if (v.get("video_page_url") or v.get("url")) not in existing_video_urls]
        filtered_count = original_count - len(videos)
        if filtered_count > 0:
            activity.logger.info(f"🔄 Filtered out {filtered_count} already-discovered videos")
    
    activity.logger.info(f"✅ Found {len(videos)} new videos")
    
    return {"videos": videos}


# =============================================================================
# Activity 3: Save Discovered Videos (No Completion Logic)
# =============================================================================

@activity.defn
async def save_discovered_videos(
    fixture_id: int,
    event_id: str,
    videos: List[Dict[str, Any]],
) -> Dict[str, Any]:
    """
    Append newly discovered videos to _discovered_videos array.
    
    Does NOT set _twitter_complete - that's handled by Download workflow
    after it knows final S3 count.
    
    Args:
        fixture_id: The fixture ID
        event_id: The event ID
        videos: List of new video dicts from Twitter (already filtered for dupes)
    
    Returns:
        Dict with save status
    
    Raises:
        RuntimeError: If MongoDB update fails
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    if not videos:
        activity.logger.info(f"No new videos to save for {event_id}")
        return {"saved": True, "video_count": 0}
    
    # Just append videos to _discovered_videos, don't touch _twitter_complete
    try:
        result = store.fixtures_active.update_one(
            {"_id": fixture_id, "events._event_id": event_id},
            {
                "$push": {
                    "events.$._discovered_videos": {"$each": videos}
                }
            }
        )
        
        if result.modified_count == 0:
            msg = f"Failed to save discovered videos for {event_id}"
            activity.logger.error(f"❌ {msg}")
            raise RuntimeError(msg)
        
        activity.logger.info(f"💾 Saved {len(videos)} new discovered videos for {event_id}")
        return {"saved": True, "video_count": len(videos)}
        
    except Exception as e:
        activity.logger.error(f"❌ Error saving videos: {e}")
        raise



