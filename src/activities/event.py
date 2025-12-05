"""Event (debounce) activities"""
from temporalio import activity
from typing import Dict, List, Any
from datetime import datetime, timezone
import hashlib
import json


def generate_event_hash(event: dict) -> str:
    """
    Generate hash from event data for stability comparison.
    Uses critical fields that shouldn't change if event is stable.
    """
    key_fields = {
        "player_id": event.get("player", {}).get("id"),
        "team_id": event.get("team", {}).get("id"),
        "type": event.get("type"),
        "detail": event.get("detail"),
        "time_elapsed": event.get("time", {}).get("elapsed"),
        "assist_id": event.get("assist", {}).get("id"),
    }
    return hashlib.md5(json.dumps(key_fields, sort_keys=True).encode()).hexdigest()


def build_twitter_search(event: dict, fixture: dict) -> str:
    """Build Twitter search string from event and fixture data"""
    player_name = event.get("player", {}).get("name", "Unknown")
    player_last_name = player_name.split()[-1] if player_name else "Unknown"
    
    # Get team name
    event_team_id = event.get("team", {}).get("id")
    home_team_id = fixture.get("teams", {}).get("home", {}).get("id")
    
    if event_team_id == home_team_id:
        team_name = fixture.get("teams", {}).get("home", {}).get("name", "")
    else:
        team_name = fixture.get("teams", {}).get("away", {}).get("name", "")
    
    return f"{player_last_name} {team_name}".strip()


def calculate_score_context(fixture: dict, event: dict) -> Dict[str, Any]:
    """
    Calculate score before and after this event.
    Returns score_before, score_after, scoring_team.
    """
    # Get all goal events in time order
    all_events = fixture.get("events", [])
    goal_events = [e for e in all_events if e.get("type") == "Goal"]
    
    # Sort by elapsed time
    goal_events.sort(key=lambda e: e.get("time", {}).get("elapsed", 0))
    
    home_team_id = fixture.get("teams", {}).get("home", {}).get("id")
    away_team_id = fixture.get("teams", {}).get("away", {}).get("id")
    
    # Find this event's position in goals
    current_elapsed = event.get("time", {}).get("elapsed", 0)
    current_player_id = event.get("player", {}).get("id")
    
    score_home = 0
    score_away = 0
    
    for goal in goal_events:
        goal_elapsed = goal.get("time", {}).get("elapsed", 0)
        goal_player_id = goal.get("player", {}).get("id")
        
        # Stop before this event
        if goal_elapsed == current_elapsed and goal_player_id == current_player_id:
            break
        
        # Count this goal
        goal_team_id = goal.get("team", {}).get("id")
        if goal_team_id == home_team_id:
            score_home += 1
        elif goal_team_id == away_team_id:
            score_away += 1
    
    score_before = {"home": score_home, "away": score_away}
    
    # Add this goal
    event_team_id = event.get("team", {}).get("id")
    if event_team_id == home_team_id:
        score_home += 1
        scoring_team = "home"
    elif event_team_id == away_team_id:
        score_away += 1
        scoring_team = "away"
    else:
        scoring_team = "unknown"
    
    score_after = {"home": score_home, "away": score_away}
    
    return {
        "_score_before": score_before,
        "_score_after": score_after,
        "_scoring_team": scoring_team
    }


@activity.defn
async def debounce_fixture_events(fixture_id: int) -> Dict[str, Any]:
    """
    Debounce events for a single fixture using iterate-and-drop pattern.
    
    Algorithm:
    1. Get live events (with _event_id generated in store_live_fixture)
    2. Get active events (with enhancement fields)
    3. Build dict of live events by _event_id for O(1) lookup
    4. Iterate through active events (what we're already tracking):
       - If event_id found in live dict:
         * Pop event from dict (mark as "processed")
         * Compare hash with last snapshot
         * If hash unchanged: increment stable_count (trigger Twitter at 3)
         * If hash changed: reset to stable_count=1 (restart debounce)
       - If event_id NOT in live dict:
         * Event removed from API (VAR disallowed goal)
         * Mark as _removed=True
    5. Whatever's left in live_events_dict = NEW events
       - Add to active with _stable_count=1
    
    This pattern eliminates set comparison overhead and makes "new events"
    detection trivial (leftover dict items).
    
    Returns:
        Dict with processing stats and list of event_ids ready for Twitter
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Get live and active data
    live_fixture = store.get_live_fixture(fixture_id)
    active_fixture = store.get_fixture_from_active(fixture_id)
    
    if not live_fixture:
        activity.logger.warning(f"Fixture {fixture_id} not found in live")
        return {"status": "not_found_live"}
    
    if not active_fixture:
        activity.logger.warning(f"Fixture {fixture_id} not found in active")
        return {"status": "not_found_active"}
    
    live_events = live_fixture.get("events", [])
    active_events = active_fixture.get("events", [])
    
    # Build dict of live events by _event_id (will pop from this as we process)
    live_events_dict = {e.get("_event_id"): e for e in live_events if e.get("_event_id")}
    
    now = datetime.now(timezone.utc)
    new_count = 0
    updated_count = 0
    completed_count = 0
    removed_count = 0
    twitter_triggered = []
    
    # Process existing active events
    for active_event in active_events:
        event_id = active_event.get("_event_id")
        if not event_id:
            continue
        
        # Check if event still exists in live
        if event_id in live_events_dict:
            # CASE 2 or 3: Event exists in both - check hash
            live_event = live_events_dict.pop(event_id)  # Remove from dict
            
            # Skip if already debounce complete
            if active_event.get("_debounce_complete", False):
                continue
            
            # Generate hashes
            live_hash = generate_event_hash(live_event)
            active_snapshots = active_event.get("_snapshots", [])
            last_hash = active_snapshots[-1]["hash"] if active_snapshots else None
            
            if live_hash == last_hash:
                # CASE 3: Hash unchanged - increment stable_count
                stable_count = active_event.get("_stable_count", 0)
                new_stable_count = stable_count + 1
                snapshot = {"timestamp": now, "hash": live_hash}
                
                store.update_event_stable_count(fixture_id, event_id, new_stable_count, snapshot)
                
                # Check if complete (3 consecutive stable polls)
                if new_stable_count >= 3:
                    store.mark_event_debounce_complete(fixture_id, event_id)
                    activity.logger.info(f"✅ DEBOUNCE COMPLETE: {event_id} (stable_count={new_stable_count}) - ready for Twitter")
                    completed_count += 1
                    twitter_triggered.append(event_id)
                else:
                    activity.logger.info(f"📊 STABLE: {event_id} (count={new_stable_count}/3)")
                
                updated_count += 1
            else:
                # CASE 2: Hash changed - reset to 1
                activity.logger.warning(f"⚠️ HASH CHANGED: {event_id}, resetting to 1")
                snapshot = {"timestamp": now, "hash": live_hash}
                store.update_event_stable_count(fixture_id, event_id, 1, snapshot)
                updated_count += 1
        else:
            # CASE 4: Event in active but NOT in live - removed from API (VAR)
            if not active_event.get("_removed", False):
                activity.logger.warning(f"🚫 REMOVED: {event_id} (VAR or API removed)")
                store.mark_event_removed(fixture_id, event_id)
                removed_count += 1
    
    # CASE 1: Whatever's left in live_events_dict are NEW events
    for event_id, live_event in live_events_dict.items():
        activity.logger.info(f"✨ NEW EVENT: {event_id}")
        
        # Generate hash
        event_hash = generate_event_hash(live_event)
        
        # Build enhancement fields
        twitter_search = build_twitter_search(live_event, live_fixture)
        score_context = calculate_score_context(live_fixture, live_event)
        
        # Create enhanced event
        enhanced_event = dict(live_event)
        enhanced_event.update({
            "_event_id": event_id,
            "_stable_count": 1,
            "_debounce_complete": False,
            "_twitter_complete": False,
            "_first_seen": now,
            "_snapshots": [{"timestamp": now, "hash": event_hash}],
            "_twitter_search": twitter_search,
            **score_context
        })
        
        # Add to active
        store.add_event_to_active(fixture_id, enhanced_event)
        activity.logger.info(
            f"  → {twitter_search} "
            f"({score_context['_score_before']} → {score_context['_score_after']})"
        )
        new_count += 1
    
    activity.logger.info(
        f"🎯 Fixture {fixture_id} debounce: "
        f"{new_count} new, {updated_count} updated, {completed_count} completed, {removed_count} removed"
    )
    
    # Build event details map for workflow naming
    event_details = {}
    for event_id in twitter_triggered:
        # Get event from active to extract player name, team, minute, and extra time
        fixture = store.get_fixture_from_active(fixture_id)
        if fixture:
            for event in fixture.get("events", []):
                if event.get("_event_id") == event_id:
                    time_data = event.get("time", {})
                    event_details[event_id] = {
                        "player": event.get("player", {}).get("name", "Unknown"),
                        "team": event.get("team", {}).get("name", "Unknown"),
                        "minute": time_data.get("elapsed", "?"),
                        "extra": time_data.get("extra")  # None if not present
                    }
                    break
    
    # Sync fixture data from live to active (preserve enhanced events)
    # This ensures fixture metadata (score, status, etc.) stays fresh
    if new_count > 0 or updated_count > 0 or completed_count > 0:
        activity.logger.info(f"🔄 Syncing fixture {fixture_id} data from live to active")
        store.sync_fixture_data(fixture_id)
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "new_events": new_count,
        "updated_events": updated_count,
        "completed_events": completed_count,
        "removed_events": removed_count,
        "twitter_triggered": twitter_triggered,
        "event_details": event_details
    }
