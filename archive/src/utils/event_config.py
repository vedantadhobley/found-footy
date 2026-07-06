"""Event type configuration - defines which events to track and scrape

Pattern mirrors fixture_status.py for consistency.
Update this file to enable/disable event types as needed.
"""

# Event type definitions with tracking and scraping rules
EVENT_TYPES = {
    "Goal": {
        "enabled": True,
        "scrapeable_details": [
            "Normal Goal",  # Regular goal
            "Penalty",      # Penalty kick goal
            "Own Goal"      # Own goal (credited to opposing player)
        ],
        "debounce_fields": ["player.name", "team.name"],  # Fields that affect Twitter search
        "debounce_stable_count": 3,  # Number of consecutive stable polls required
        "description": "Goal events - triggers Twitter video search"
    },
    
    # Future phases (disabled for now - set enabled: True to activate)
    "Card": {
        "enabled": False,
        "scrapeable_details": ["Red card"],  # Only red cards are Twitter-worthy
        "debounce_fields": ["player.name", "team.name"],
        "debounce_stable_count": 3,
        "description": "Card events - red cards for highlight videos"
    },
    
    "Var": {
        "enabled": False,
        "scrapeable_details": [],  # Don't scrape VAR, just track for UI
        "debounce_fields": [],
        "debounce_stable_count": 1,  # No debounce for VAR
        "description": "VAR decisions - for UI display only"
    },
    
    "subst": {
        "enabled": False,
        "scrapeable_details": [],
        "debounce_fields": [],
        "debounce_stable_count": 0,
        "description": "Substitutions - not tracked"
    }
}


def should_track_event(event: dict) -> bool:
    """
    Determine if an event should be tracked based on type and detail.
    
    Args:
        event: Event dict from API with 'type' and 'detail' fields
        
    Returns:
        True if event should be tracked and processed
        
    Examples:
        >>> should_track_event({"type": "Goal", "detail": "Normal Goal"})
        True
        >>> should_track_event({"type": "Goal", "detail": "Missed Penalty"})
        False
        >>> should_track_event({"type": "subst", "detail": "Substitution 1"})
        False
    """
    event_type = event.get("type")
    event_detail = event.get("detail")
    comments = event.get("comments", "")
    
    config = EVENT_TYPES.get(event_type, {})
    
    # Check if type is enabled
    if not config.get("enabled", False):
        return False
    
    # Filter out penalty shootout goals (not regular match goals)
    if event_type == "Goal" and comments and "Penalty Shootout" in comments:
        return False
    
    # Check if detail is in scrapeable list (if list exists)
    scrapeable_details = config.get("scrapeable_details", [])
    if scrapeable_details:
        return event_detail in scrapeable_details
    
    # If no scrapeable_details list, track all events of this type
    return True


# Constant for debounce stable count (default across all event types)
DEBOUNCE_STABLE_COUNT = 3
