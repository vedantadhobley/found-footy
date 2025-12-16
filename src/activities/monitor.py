"""Monitor activities"""
from temporalio import activity
from typing import Dict, List, Any
from datetime import datetime, timezone


@activity.defn
async def activate_fixtures() -> Dict[str, int]:
    """
    Move fixtures from staging to active if start time reached.
    Fixtures are stored with empty events array - will be populated by monitor.
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    now = datetime.now(timezone.utc)
    
    activity.logger.info(f"🕐 Checking for fixtures to activate (current time: {now.isoformat()})")
    
    # Get all fixtures from staging
    staging_fixtures = store.get_staging_fixtures()
    
    if not staging_fixtures:
        activity.logger.info("📋 No fixtures in staging")
        return {"activated_count": 0}
    
    activity.logger.info(f"📋 Found {len(staging_fixtures)} fixtures in staging")
    
    activated_count = 0
    
    for fixture in staging_fixtures:
        try:
            fixture_id = fixture["_id"]
            fixture_date_str = fixture.get("fixture", {}).get("date")
            
            if not fixture_date_str:
                continue
            
            # Parse fixture date
            fixture_date = datetime.fromisoformat(fixture_date_str.replace('Z', '+00:00'))
            
            # Check if fixture should be active (start time reached)
            if fixture_date <= now:
                # Move to active
                if store.activate_fixture(fixture_id):
                    home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
                    away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
                    activity.logger.info(f"✅ Activated fixture {fixture_id}: {home_team} vs {away_team}")
                    activated_count += 1
        
        except Exception as e:
            activity.logger.error(f"❌ Error activating fixture {fixture.get('_id')}: {e}")
            continue
    
    activity.logger.info(f"🎯 Activated {activated_count} fixtures")
    
    return {"activated_count": activated_count}


@activity.defn
async def fetch_active_fixtures() -> List[Dict[str, Any]]:
    """
    Batch fetch all active fixtures from API-Football.
    Returns raw API data with events.
    """
    from src.api.api_client import fixtures_batch
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Get all active fixture IDs
    fixture_ids = store.get_active_fixture_ids()
    
    if not fixture_ids:
        activity.logger.info("📋 No active fixtures to fetch")
        return []
    
    activity.logger.info(f"🌐 Batch fetching data for {len(fixture_ids)} active fixtures")
    
    try:
        fresh_data = fixtures_batch(fixture_ids)
        activity.logger.info(f"✅ Retrieved fresh data for {len(fresh_data)} fixtures")
        return fresh_data
    
    except Exception as e:
        activity.logger.error(f"❌ Failed to batch fetch fixtures: {e}")
        raise


@activity.defn
async def store_and_compare(fixture_id: int, fixture_data: Dict) -> Dict[str, Any]:
    """
    Store fixture in fixtures_live and compare with fixtures_active.
    
    Returns comparison result with:
    - needs_debounce: True if there are NEW/INCOMPLETE/REMOVED events
    - new_events: Count of new events
    - incomplete_events: Count of events with data changes
    - removed_events: Count of events removed from live
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        # Store in fixtures_live (raw API data with all events)
        store.store_live_fixture(fixture_id, fixture_data)
        activity.logger.info(f"📥 Stored live data for fixture {fixture_id}")
        
        # Compare live vs active
        comparison = store.compare_live_vs_active(fixture_id)
        
        if comparison["needs_debounce"]:
            activity.logger.info(
                f"🎯 Fixture {fixture_id} needs debounce: "
                f"NEW={comparison['new_events']}, "
                f"INCOMPLETE={comparison['incomplete_events']}, "
                f"REMOVED={comparison['removed_events']}"
            )
        
        return comparison
    
    except Exception as e:
        activity.logger.error(f"❌ Error processing fixture {fixture_id}: {e}")
        raise


@activity.defn
async def complete_fixture_if_ready(fixture_id: int) -> bool:
    """
    Check if fixture is ready to complete:
    - Status must be FT/AET/PEN
    - All events must have _monitor_complete=True
    - All events must have _twitter_complete=True
    
    If ready, moves fixture to fixtures_completed.
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        # Check if all events are complete and move if ready
        if store.complete_fixture(fixture_id):
            activity.logger.info(f"🏁 Moved fixture {fixture_id} to completed")
            return True
        else:
            # Log why it's not ready (for debugging)
            fixture = store.get_fixture_from_active(fixture_id)
            if fixture:
                events = fixture.get("events", [])
                enhanced_events = [e for e in events if e.get("_event_id")]
                
                if enhanced_events:
                    debounced = sum(1 for e in enhanced_events if e.get("_monitor_complete"))
                    twitter_done = sum(1 for e in enhanced_events if e.get("_twitter_complete"))
                    activity.logger.debug(
                        f"Fixture {fixture_id} waiting: "
                        f"{debounced}/{len(enhanced_events)} debounced, "
                        f"{twitter_done}/{len(enhanced_events)} Twitter complete"
                    )
            return False
    
    except Exception as e:
        activity.logger.error(f"❌ Error completing fixture {fixture_id}: {e}")
        return False


@activity.defn
async def process_fixture_events(fixture_id: int) -> Dict[str, Any]:
    """
    Process fixture events using pure set comparison - NO HASH NEEDED!
    
    With player_id in event_id, VAR scenarios are handled automatically:
    - Player changes → different event_id → old marked removed, new added
    - Goal cancelled → event_id disappears → marked removed
    
    Algorithm:
    1. Get event_ids from live and active (sets)
    2. NEW = live_ids - active_ids → add with stable_count=1
    3. REMOVED = active_ids - live_ids → mark as removed
    4. MATCHING = live_ids & active_ids → increment stable_count
    5. For stable_count >= 3 → mark debounce_complete, trigger Twitter
    6. Sync fixture metadata
    """
    from src.data.mongo_store import FootyMongoStore
    from src.utils.event_enhancement import build_twitter_search, calculate_score_context
    
    store = FootyMongoStore()
    
    live_fixture = store.get_live_fixture(fixture_id)
    active_fixture = store.get_fixture_from_active(fixture_id)
    
    if not live_fixture:
        activity.logger.warning(f"Fixture {fixture_id} not in live")
        return {"status": "not_found_live"}
    
    if not active_fixture:
        activity.logger.warning(f"Fixture {fixture_id} not in active")
        return {"status": "not_found_active"}
    
    live_events = live_fixture.get("events", [])
    active_events = active_fixture.get("events", [])
    
    # Build sets for comparison
    # Note: VAR'd events are DELETED (not marked _removed), so no special handling needed
    live_ids = {e["_event_id"] for e in live_events if e.get("_event_id")}
    active_map = {e["_event_id"]: e for e in active_events if e.get("_event_id")}
    active_ids = set(active_map.keys())
    
    # NEW events
    new_ids = live_ids - active_ids
    new_count = 0
    for event_id in new_ids:
        live_event = next(e for e in live_events if e.get("_event_id") == event_id)
        
        # Build enhancement fields
        twitter_search = build_twitter_search(live_event, live_fixture)
        score_context = calculate_score_context(live_fixture, live_event)
        
        # Generate display titles for frontend
        title, subtitle = store._generate_event_display_titles(live_fixture, live_event)
        
        enhanced = {
            **live_event,
            # Monitor tracking (counter-based) - Monitor is the orchestrator
            "_monitor_count": 1,
            "_monitor_complete": False,  # Set to True when _monitor_count >= 3
            # Twitter tracking - Monitor increments count, Twitter workflow sets complete
            "_twitter_count": 0,  # Monitor increments when triggering Twitter
            "_twitter_complete": False,  # Twitter workflow sets True when done (incl downloads)
            "_twitter_search": twitter_search,
            # Video storage (accumulated across all attempts)
            "_discovered_videos": [],
            # _s3_videos: List of {url, perceptual_hash, resolution_score, popularity, rank}
            "_s3_videos": [],
            # Display fields (for frontend)
            "_display_title": title,
            "_display_subtitle": subtitle,
            # Metadata
            **score_context,
            "_removed": False,
            "_first_seen": datetime.now(timezone.utc),
        }
        
        first_seen = enhanced["_first_seen"]
        if store.add_event_to_active(fixture_id, enhanced, first_seen):
            activity.logger.info(f"✨ NEW EVENT: {event_id}")
            new_count += 1
    
    # REMOVED events (VAR cancelled) - DELETE from MongoDB and S3
    removed_ids = active_ids - live_ids
    removed_count = 0
    for event_id in removed_ids:
        if store.mark_event_removed(fixture_id, event_id):
            activity.logger.warning(f"🗑️ VAR REMOVED: {event_id} (deleted from DB + S3)")
            removed_count += 1
    
    # MATCHING events - increment stable_count
    matching_ids = live_ids & active_ids
    updated_count = 0
    twitter_triggered = []
    twitter_retry_needed = []
    
    for event_id in matching_ids:
        active_event = active_map[event_id]
        
        # =====================================================================
        # CASE 1: _monitor_complete = TRUE -> Check Twitter status
        # =====================================================================
        if active_event.get("_monitor_complete"):
            # Already through debounce - check if we need more Twitter attempts
            if not active_event.get("_twitter_complete"):
                twitter_count = active_event.get("_twitter_count", 0)
                
                # Check if we need more Twitter attempts (max 3)
                if twitter_count < 3:
                    # Increment counter for this attempt (Monitor tracks count)
                    new_twitter_count = twitter_count + 1
                    store.update_event_twitter_count(fixture_id, event_id, new_twitter_count)
                    
                    live_event = next(e for e in live_events if e.get("_event_id") == event_id)
                    twitter_retry_needed.append({
                        "event_id": event_id,
                        "player_name": live_event.get("player", {}).get("name", "Unknown"),
                        "team_name": live_event.get("team", {}).get("name", "Unknown"),
                        "minute": live_event.get("time", {}).get("elapsed"),
                        "extra": live_event.get("time", {}).get("extra"),
                        "attempt_number": new_twitter_count,
                    })
                    activity.logger.info(
                        f"🔄 TWITTER ATTEMPT: {event_id} (#{new_twitter_count}/3)"
                    )
                # Note: _twitter_complete is set by Twitter workflow when it finishes
            continue  # Already debounced, skip monitor count processing
        
        # =====================================================================
        # CASE 2: _monitor_complete = FALSE -> Check/increment monitor count
        # =====================================================================
        # Increment monitor count
        new_count_val = active_event.get("_monitor_count", 0) + 1
        
        # Note: No hash/snapshot needed with player_id approach!
        if store.update_event_stable_count(fixture_id, event_id, new_count_val, None):
            updated_count += 1
            
            if new_count_val >= 3:
                # Mark complete and prepare for Twitter
                # Pass _first_seen to update _last_activity (when goal was first detected)
                first_seen = active_event.get("_first_seen")
                store.mark_event_monitor_complete(fixture_id, event_id, first_seen)
                
                live_event = next(e for e in live_events if e.get("_event_id") == event_id)
                twitter_triggered.append({
                    "event_id": event_id,
                    "player_name": live_event.get("player", {}).get("name", "Unknown"),
                    "team_name": live_event.get("team", {}).get("name", "Unknown"),
                    "minute": live_event.get("time", {}).get("elapsed"),
                    "extra": live_event.get("time", {}).get("extra"),
                })
                activity.logger.info(f"✅ MONITOR COMPLETE: {event_id} (monitor_count=3)")
            else:
                activity.logger.info(f"📊 MONITORING: {event_id} (count={new_count_val}/3)")
    
    # Sync fixture metadata
    store.sync_fixture_data(fixture_id)
    
    return {
        "status": "success",
        "new_events": new_count,
        "removed_events": removed_count,
        "updated_events": updated_count,
        "twitter_triggered": twitter_triggered,
        "twitter_retry_needed": twitter_retry_needed,
    }


@activity.defn
async def sync_fixture_metadata(fixture_id: int) -> bool:
    """
    Sync fixture top-level data from fixtures_live to fixtures_active.
    Called when no debounce is needed but we want to keep fixture metadata fresh.
    
    Updates: score, status, time, teams data, etc.
    Preserves: Enhanced events array
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    try:
        if store.sync_fixture_data(fixture_id):
            activity.logger.debug(f"🔄 Synced fixture {fixture_id} metadata")
            return True
        return False
    except Exception as e:
        activity.logger.error(f"❌ Error syncing fixture {fixture_id}: {e}")
        return False


@activity.defn
async def notify_frontend_refresh() -> bool:
    """
    Notify the frontend API to broadcast a refresh to all connected SSE clients.
    Called at the end of monitor cycles and after download completions.
    """
    import os
    import requests
    
    api_url = os.getenv("FRONTEND_API_URL", "http://vedanta-systems-dev-found-footy-api:3001")
    
    try:
        response = requests.post(f"{api_url}/api/found-footy/refresh", timeout=5)
        if response.ok:
            result = response.json()
            activity.logger.info(f"📡 Frontend notified ({result.get('clientsNotified', 0)} clients)")
            return True
        else:
            activity.logger.warning(f"⚠️ Frontend notify failed: {response.status_code}")
            return False
    except requests.exceptions.ConnectionError:
        # Frontend not running is not a fatal error
        activity.logger.debug("📡 Frontend API not available (connection refused)")
        return False
    except Exception as e:
        activity.logger.warning(f"⚠️ Frontend notify error: {e}")
        return False