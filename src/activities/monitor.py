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
    - All events must have _debounce_complete=True
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
                    debounced = sum(1 for e in enhanced_events if e.get("_debounce_complete"))
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
