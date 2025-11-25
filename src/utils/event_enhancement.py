"""Event enhancement utilities - add metadata to raw API events

This module handles:
- Filtering raw API events to trackable events only
- Assigning unique event IDs
- Calculating score context (before/after for each event)
- Generating search hashes
- Comparing with stored events for debounce logic
"""

from typing import List, Optional
from src.utils.event_config import should_track_event, get_event_type_for_id, DEBOUNCE_STABLE_COUNT


def filter_trackable_events(raw_events: List[dict]) -> List[dict]:
    """
    Filter raw API events to only trackable ones.
    
    Args:
        raw_events: List of all events from API
        
    Returns:
        List of events that should be tracked (Goals, Red Cards)
    """
    return [event for event in raw_events if should_track_event(event)]


def assign_event_ids(events: List[dict], fixture_id: int) -> List[dict]:
    """
    Assign unique event IDs to events based on team and type.
    
    Event ID format: {fixture_id}_{team_id}_{event_type}_{#}
    Example: 1378993_40_Goal_1
    
    Args:
        events: List of events (must be sorted chronologically)
        fixture_id: Fixture ID
        
    Returns:
        Events with _event_id field added
    """
    # Count events by (team_id, type) to assign sequential numbers
    counters = {}
    
    enhanced_events = []
    for event in events:
        team_id = event.get("team", {}).get("id")
        event_type = get_event_type_for_id(event)
        
        # Track counter for this team+type
        key = (team_id, event_type)
        counters[key] = counters.get(key, 0) + 1
        
        # Generate event ID
        event_id = f"{fixture_id}_{team_id}_{event_type}_{counters[key]}"
        
        # Add to event
        event_copy = event.copy()
        event_copy["_event_id"] = event_id
        enhanced_events.append(event_copy)
    
    return enhanced_events


def calculate_score_context(
    events: List[dict],
    home_team_id: int,
    away_team_id: int
) -> List[dict]:
    """
    Calculate score after each event chronologically.
    
    Since API already tells us which team scored (event.team.id),
    we just need to track the current score, not before/after.
    
    Args:
        events: List of events (should have _event_id already)
        home_team_id: Home team ID
        away_team_id: Away team ID
        
    Returns:
        Events with _score field (score after this event)
    """
    home_score = 0
    away_score = 0
    
    enhanced_events = []
    for event in events:
        event_copy = event.copy()
        
        # Update score if this is a goal
        event_type = event.get("type")
        if event_type == "Goal":
            team_id = event.get("team", {}).get("id")
            
            # Increment score for the team that scored
            if team_id == home_team_id:
                home_score += 1
            elif team_id == away_team_id:
                away_score += 1
        
        # Store current score after this event
        event_copy["_score"] = {"home": home_score, "away": away_score}
        
        enhanced_events.append(event_copy)
    
    return enhanced_events


def generate_search_hash(event: dict) -> str:
    """
    Generate search hash for debounce comparison.
    
    Hash includes: player name + team name (lowercased, trimmed)
    
    Args:
        event: Event data
        
    Returns:
        Hash string like "szoboszlai_liverpool"
    """
    player_name = event.get("player", {}).get("name", "").lower().strip()
    team_name = event.get("team", {}).get("name", "").lower().strip()
    
    # For cards, we might not have a player (team card?)
    if not player_name:
        player_name = "unknown"
    
    return f"{player_name}_{team_name}"


def enhance_events_with_metadata(
    raw_events: List[dict],
    fixture_id: int,
    home_team_id: int,
    away_team_id: int,
    stored_events: Optional[List[dict]] = None
) -> List[dict]:
    """
    Full enhancement pipeline: filter → assign IDs → calculate scores → add metadata.
    
    Args:
        raw_events: All events from API (unsorted)
        fixture_id: Fixture ID
        home_team_id: Home team ID
        away_team_id: Away team ID
        stored_events: Previously stored events (for debounce comparison)
        
    Returns:
        Fully enhanced events ready to store in fixtures_active
    """
    # 1. Filter to trackable events only
    trackable = filter_trackable_events(raw_events)
    
    # 2. Sort chronologically by time.elapsed
    sorted_events = sorted(trackable, key=lambda e: e.get("time", {}).get("elapsed", 0))
    
    # 3. Assign event IDs
    with_ids = assign_event_ids(sorted_events, fixture_id)
    
    # 4. Calculate score context
    with_scores = calculate_score_context(with_ids, home_team_id, away_team_id)
    
    # 5. Add search hash and debounce fields
    enhanced = []
    for event in with_scores:
        event_copy = event.copy()
        
        # Generate search hash
        event_copy["_search_hash"] = generate_search_hash(event)
        
        # Initialize debounce fields
        event_copy["_debounce_count"] = 0
        event_copy["_debounce_complete"] = False
        
        # Initialize Twitter fields
        event_copy["_twitter_complete"] = False
        event_copy["_discovered_videos"] = []
        
        enhanced.append(event_copy)
    
    # 6. If we have stored events, update debounce counts
    if stored_events:
        enhanced = compare_with_stored_events(enhanced, stored_events)
    
    return enhanced


def compare_with_stored_events(
    fresh_events: List[dict],
    stored_events: List[dict]
) -> List[dict]:
    """
    Compare fresh events with stored events to update debounce counts.
    
    IMPORTANT: This automatically drops events that no longer exist in the API.
    Only events present in fresh_events (current API response) will be returned.
    This handles cases where API removes events (e.g., VAR disallows a goal).
    
    Logic:
    - Match by _event_id
    - If _search_hash unchanged: increment _debounce_count
    - If _search_hash changed: reset to 0
    - If count >= DEBOUNCE_STABLE_COUNT: mark _debounce_complete
    - Preserve _twitter_complete and _discovered_videos from stored
    
    Args:
        fresh_events: Newly fetched and enhanced events (ONLY events in current API)
        stored_events: Previously stored events with debounce state
        
    Returns:
        Fresh events with updated debounce state (old events automatically dropped)
    """
    # Index stored events by event_id
    stored_by_id = {
        e.get("_event_id"): e
        for e in stored_events
        if e.get("_event_id")
    }
    
    updated = []
    for fresh in fresh_events:
        event_id = fresh.get("_event_id")
        stored = stored_by_id.get(event_id)
        
        if not stored:
            # New event - keep initial values
            updated.append(fresh)
            continue
        
        # Event exists - check if hash changed
        fresh_hash = fresh.get("_search_hash")
        stored_hash = stored.get("_search_hash")
        
        if fresh_hash == stored_hash:
            # Hash unchanged - increment count
            count = stored.get("_debounce_count", 0) + 1
            fresh["_debounce_count"] = count
            fresh["_debounce_complete"] = (count >= DEBOUNCE_STABLE_COUNT)
        else:
            # Hash changed - reset
            fresh["_debounce_count"] = 0
            fresh["_debounce_complete"] = False
        
        # Preserve Twitter state from stored event
        fresh["_twitter_complete"] = stored.get("_twitter_complete", False)
        fresh["_discovered_videos"] = stored.get("_discovered_videos", [])
        
        updated.append(fresh)
    
    # Events in stored_events but NOT in fresh_events are dropped here
    # This is intentional and correct behavior
    
    return updated


def get_events_ready_for_twitter(events: List[dict]) -> List[dict]:
    """
    Get events that are ready for Twitter search.
    
    Criteria:
    - _debounce_complete = True
    - _twitter_complete = False
    
    Args:
        events: List of enhanced events
        
    Returns:
        Events ready for Twitter search
    """
    return [
        e for e in events
        if e.get("_debounce_complete") and not e.get("_twitter_complete")
    ]


def get_incomplete_fixtures(fixtures: List[dict]) -> List[dict]:
    """
    Get fixtures that still have incomplete events.
    
    A fixture is incomplete if it has any events with:
    - _debounce_complete = False OR
    - _twitter_complete = False
    
    Args:
        fixtures: List of fixtures with events array
        
    Returns:
        Fixtures that are not yet complete
    """
    incomplete = []
    for fixture in fixtures:
        events = fixture.get("events", [])
        
        # Check if any event is incomplete
        has_incomplete = any(
            not e.get("_debounce_complete") or not e.get("_twitter_complete")
            for e in events
            if e.get("_event_id")  # Only check enhanced events
        )
        
        if has_incomplete:
            incomplete.append(fixture)
    
    return incomplete
