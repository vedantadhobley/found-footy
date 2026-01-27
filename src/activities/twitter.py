"""
Twitter Activities - Granular video discovery pipeline

Three-activity pattern for Twitter search with proper retry semantics:
1. get_twitter_search_data - Fetch event and search query from MongoDB
2. execute_twitter_search - POST to Firefox automation (the risky call)
3. save_twitter_results - Persist results back to MongoDB

Logging Convention: All logs prefixed with [TWITTER] for easy filtering.

This separation ensures:
- Twitter search failures trigger retry (not swallowed)
- Successful searches don't get lost if save fails
- Clear visibility in Temporal UI of which step failed
"""
from temporalio import activity
from typing import Dict, List, Any, Optional
import os
import requests
import itertools

from src.data.models import EventFields


# Round-robin counter for distributing requests across Twitter instances
# Using itertools.cycle for thread-safe round-robin selection
_twitter_instance_cycle = None
_twitter_healthy_cache = []
_twitter_cache_time = 0


# =============================================================================
# Workflow Tracking Activities
# =============================================================================

@activity.defn
async def set_monitor_complete(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Set _monitor_complete = true for an event.
    
    Called by TwitterWorkflow at the VERY START to confirm it actually started.
    This ensures the flag is only set when Twitter workflow ACTUALLY STARTS running,
    not just when MonitorWorkflow attempts to spawn it.
    
    If Twitter fails to start (Temporal issue, etc.), _monitor_complete stays false,
    and the next monitor workflow will see (count >= 3 AND complete = false) → retry spawn.
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
    
    Returns:
        Dict with success
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        success = store.mark_monitor_complete(fixture_id, event_id)
        activity.logger.info(
            f"✅ [TWITTER] set_monitor_complete | event={event_id} | success={success}"
        )
        return {"success": success}
    except Exception as e:
        activity.logger.error(
            f"❌ [TWITTER] set_monitor_complete failed | event={event_id} | error={e}"
        )
        return {"success": False}


@activity.defn
async def get_download_workflow_count(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Get the current count of download workflows that have run for this event.
    
    Called by TwitterWorkflow at the start of each iteration to determine if
    we've reached 10 download workflows (and can exit the loop).
    
    Args:
        fixture_id: Fixture ID
        event_id: Event ID
    
    Returns:
        Dict with count
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        count = store.get_download_workflow_count(fixture_id, event_id)
        activity.logger.info(
            f"📊 [TWITTER] get_download_workflow_count | event={event_id} | count={count}"
        )
        return {"count": count}
    except Exception as e:
        activity.logger.error(
            f"❌ [TWITTER] get_download_workflow_count failed | event={event_id} | error={e}"
        )
        return {"count": 0}


# =============================================================================
# Activity 0: Check Event Exists (for workflow graceful termination)
# =============================================================================

@activity.defn
async def check_event_exists(fixture_id: int, event_id: str) -> Dict[str, Any]:
    """
    Check if an event still exists in MongoDB.
    
    Used by TwitterWorkflow at the start of each attempt to gracefully
    terminate if the event was deleted (VAR - count hit 0).
    
    We only check existence, not monitor_complete, because:
    - Once monitor_complete=True, it stays True forever (one-way latch)
    - If the event exists, the workflow should continue
    - If the event was deleted (count decremented to 0), workflow should stop
    
    Args:
        fixture_id: The fixture ID
        event_id: The event ID to check
    
    Returns:
        Dict with:
        - exists: True if event still in MongoDB
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    activity.logger.info(f"🔍 [TWITTER] check_event_exists | fixture={fixture_id} | event={event_id}")
    
    # Check in active collection
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        activity.logger.warning(f"⚠️ [TWITTER] Fixture {fixture_id} not found in active")
        return {"exists": False}
    
    # Find the specific event
    for evt in fixture.get("events", []):
        if evt.get(EventFields.EVENT_ID) == event_id:
            activity.logger.info(f"✅ [TWITTER] Event exists | event={event_id}")
            return {"exists": True}
    
    activity.logger.warning(f"⚠️ [TWITTER] Event NOT FOUND | fixture={fixture_id} | event={event_id}")
    return {"exists": False}


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
    
    activity.logger.info(f"🔍 [TWITTER] get_twitter_search_data | fixture={fixture_id} | event={event_id}")
    
    # Fetch fixture from active collection
    fixture = store.get_fixture_from_active(fixture_id)
    if not fixture:
        msg = f"[TWITTER] Fixture {fixture_id} not found in fixtures_active"
        activity.logger.error(f"❌ {msg}")
        raise ValueError(msg)
    
    # Find the specific event
    event = None
    for evt in fixture.get("events", []):
        if evt.get(EventFields.EVENT_ID) == event_id:
            event = evt
            break
    
    if not event:
        msg = f"[TWITTER] Event {event_id} not found in fixture {fixture_id}"
        activity.logger.error(f"❌ {msg}")
        raise ValueError(msg)
    
    # Get prebuilt search string (set by process_fixture_events)
    twitter_search = event.get(EventFields.TWITTER_SEARCH, "")
    if not twitter_search:
        msg = f"[TWITTER] No _twitter_search field on event {event_id}"
        activity.logger.error(f"❌ {msg}")
        raise ValueError(msg)
    
    # Get existing discovered videos (for retry deduplication)
    existing_videos = event.get(EventFields.DISCOVERED_VIDEOS, [])
    # Twitter service uses video_page_url field
    existing_urls = [v.get("video_page_url") or v.get("url") for v in existing_videos if v.get("video_page_url") or v.get("url")]
    
    # Get match date for filtering (only search tweets from around match time)
    match_date = fixture.get("fixture", {}).get("date", "")
    
    activity.logger.info(
        f"✅ [TWITTER] Search data ready | query='{twitter_search}' | "
        f"existing_urls={len(existing_urls)} | event={event_id}"
    )
    
    return {
        "twitter_search": twitter_search,
        "fixture_id": fixture_id,
        "event_id": event_id,
        "existing_video_urls": existing_urls,
        "match_date": match_date,  # ISO format: 2025-12-27T15:00:00+00:00
    }


# =============================================================================
# Activity 2: Execute Twitter Search (The Risky External Call)
# =============================================================================

@activity.defn
async def execute_twitter_search(
    twitter_search: str, 
    existing_video_urls: Optional[List[str]] = None,
    max_age_minutes: int = 5
) -> Dict[str, Any]:
    """
    POST to Twitter browser automation service.
    
    This is the risky external call that needs proper retry policy.
    If this fails, Temporal will retry the activity (not swallow the error).
    
    Sends heartbeats during the request to signal progress to Temporal.
    Browser automation can take 2+ minutes - heartbeats prevent false timeouts.
    
    Passes exclude_urls to Twitter service so it can skip already-discovered
    videos during scraping, allowing us to find more NEW videos.
    
    Uses time-based scrolling: scrolls through "Latest" results until finding
    a tweet older than max_age_minutes, then stops. Returns ALL videos found.
    
    Args:
        twitter_search: Search query (e.g., "Salah Liverpool")
        existing_video_urls: List of video URLs already discovered (passed as exclude_urls)
        max_age_minutes: Stop scrolling when tweet is older than this (default: 5)
    
    Returns:
        Dict with videos array (all NEW videos not in exclude_urls)
    
    Raises:
        ConnectionError: Twitter service unavailable
        TimeoutError: Search took too long
        RuntimeError: Non-200 response from service (including 503 auth required)
    """
    import asyncio
    import time
    
    global _twitter_instance_cycle, _twitter_healthy_cache, _twitter_cache_time
    
    # Discover healthy Twitter instances dynamically
    # Check all possible instances (1-8) and use healthy ones
    # Cache health check results for 30 seconds to reduce overhead
    current_time = time.time()
    
    if current_time - _twitter_cache_time > 30 or not _twitter_healthy_cache:
        all_twitter_urls = [
            f"http://found-footy-prod-twitter-{i}:8888"
            for i in range(1, 9)
        ]
        
        # Quick health check to find running instances (2s timeout)
        healthy_urls = []
        for url in all_twitter_urls:
            try:
                resp = requests.get(f"{url}/health", timeout=2)
                if resp.status_code == 200:
                    healthy_urls.append(url)
            except:
                pass  # Instance not running
        
        # Fallback to first 2 if none respond (shouldn't happen)
        if not healthy_urls:
            healthy_urls = all_twitter_urls[:2]
        
        # Update cache and reset round-robin cycle if instances changed
        if set(healthy_urls) != set(_twitter_healthy_cache):
            _twitter_instance_cycle = itertools.cycle(healthy_urls)
            activity.logger.info(f"🔄 [TWITTER] Instance pool changed: {len(healthy_urls)} instances")
        
        _twitter_healthy_cache = healthy_urls
        _twitter_cache_time = current_time
    else:
        healthy_urls = _twitter_healthy_cache
    
    # Round-robin selection (guaranteed even distribution)
    if _twitter_instance_cycle is None:
        _twitter_instance_cycle = itertools.cycle(healthy_urls)
    session_url = next(_twitter_instance_cycle)
    
    exclude_urls = existing_video_urls or []
    
    activity.logger.info(
        f"🐦 [TWITTER] execute_twitter_search | query='{twitter_search}' | "
        f"max_age={max_age_minutes}min | excluding={len(exclude_urls)} URLs | "
        f"instance={session_url} | healthy={len(healthy_urls)}"
    )
    
    # Send heartbeat before starting the request
    activity.heartbeat(f"Starting search: {twitter_search}")
    
    # Use a background task to send heartbeats during the long HTTP request
    heartbeat_task = None
    search_complete = False
    
    async def heartbeat_loop():
        """Send heartbeat every 15 seconds while waiting for browser automation"""
        count = 0
        while not search_complete:
            await asyncio.sleep(15)
            if not search_complete:
                count += 1
                activity.heartbeat(f"Searching... ({count * 15}s elapsed)")
                activity.logger.debug(f"💓 [TWITTER] Heartbeat #{count} | query='{twitter_search}'")
    
    try:
        # Start heartbeat loop in background
        heartbeat_task = asyncio.create_task(heartbeat_loop())
        
        activity.logger.info(f"📡 [TWITTER] POST {session_url}/search")
        
        response = requests.post(
            f"{session_url}/search",
            json={
                "search_query": twitter_search, 
                "exclude_urls": exclude_urls,
                "max_age_minutes": max_age_minutes,
            },
            timeout=120,  # 2 min for browser automation
        )
        
        search_complete = True
        
    except requests.exceptions.ConnectionError as e:
        search_complete = True
        activity.logger.error(f"❌ [TWITTER] Service UNREACHABLE | url={session_url} | error={e}")
        raise ConnectionError(f"Twitter service at {session_url} unreachable: {e}")
    except requests.exceptions.Timeout:
        search_complete = True
        activity.logger.error(f"❌ [TWITTER] Search TIMEOUT after 120s | query='{twitter_search}'")
        raise TimeoutError(f"Twitter search timed out for '{twitter_search}'")
    finally:
        search_complete = True
        if heartbeat_task:
            heartbeat_task.cancel()
            try:
                await heartbeat_task
            except asyncio.CancelledError:
                pass
    
    # Check for 503 - this means authentication is required
    if response.status_code == 503:
        try:
            error_data = response.json()
            error_msg = error_data.get("detail", {}).get("message", "Authentication required")
        except:
            error_msg = "Twitter authentication required - manual login needed"
        
        activity.logger.error(f"❌ [TWITTER] AUTH FAILED | query='{twitter_search}' | error={error_msg}")
        activity.logger.error(f"   🔐 [TWITTER] Open VNC at http://localhost:4103 to login")
        raise RuntimeError(f"Twitter authentication required: {error_msg}")
    
    # Check other error responses
    if response.status_code != 200:
        activity.logger.error(
            f"❌ [TWITTER] Service ERROR | status={response.status_code} | "
            f"query='{twitter_search}' | response={response.text[:200]}"
        )
        raise RuntimeError(f"Twitter service error {response.status_code}: {response.text}")
    
    # Parse videos from response
    data = response.json()
    videos = data.get("videos", [])
    
    activity.logger.info(
        f"✅ [TWITTER] Search SUCCESS | query='{twitter_search}' | "
        f"found={len(videos)} videos"
    )
    
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
    
    activity.logger.info(
        f"💾 [TWITTER] save_discovered_videos | fixture={fixture_id} | "
        f"event={event_id} | count={len(videos)}"
    )
    
    if not videos:
        activity.logger.info(f"📭 [TWITTER] No videos to save | event={event_id}")
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
            msg = f"[TWITTER] Failed to save discovered videos | event={event_id}"
            activity.logger.error(f"❌ {msg}")
            raise RuntimeError(msg)
        
        activity.logger.info(
            f"✅ [TWITTER] Saved {len(videos)} discovered video URLs | event={event_id}"
        )
        return {"saved": True, "video_count": len(videos)}
        
    except Exception as e:
        activity.logger.error(
            f"❌ [TWITTER] save_discovered_videos FAILED | event={event_id} | error={e}"
        )
        raise


# =============================================================================
# Activity 4: Update Twitter Attempt Counter

