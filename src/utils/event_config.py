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


def get_event_config(event_type: str) -> dict:
    """Get configuration for an event type"""
    return EVENT_TYPES.get(event_type, {})


def is_event_type_enabled(event_type: str) -> bool:
    """Check if event type is enabled for tracking"""
    config = EVENT_TYPES.get(event_type, {})
    return config.get("enabled", False)


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


def should_scrape_twitter(event: dict) -> bool:
    """
    Determine if event should trigger Twitter scraping.
    
    Args:
        event: Event dict from API
        
    Returns:
        True if event should trigger Twitter video search
    """
    event_type = event.get("type")
    event_detail = event.get("detail")
    
    config = EVENT_TYPES.get(event_type, {})
    
    if not config.get("enabled", False):
        return False
    
    scrapeable_details = config.get("scrapeable_details", [])
    return event_detail in scrapeable_details


def get_scrapeable_details(event_type: str) -> list:
    """Get list of scrapeable details for an event type"""
    config = EVENT_TYPES.get(event_type, {})
    return config.get("scrapeable_details", [])


def get_debounce_fields(event_type: str) -> list:
    """Get list of fields to use for debounce hashing"""
    config = EVENT_TYPES.get(event_type, {})
    return config.get("debounce_fields", [])


def get_debounce_stable_count(event_type: str) -> int:
    """Get number of stable polls required for confirmation"""
    config = EVENT_TYPES.get(event_type, {})
    return config.get("debounce_stable_count", 3)


def get_enabled_event_types() -> list:
    """Get list of enabled event types"""
    return [
        event_type 
        for event_type, config in EVENT_TYPES.items() 
        if config.get("enabled", False)
    ]


def get_all_event_types() -> dict:
    """Get all event type configurations"""
    return EVENT_TYPES


# Convenience functions for specific checks
def is_goal_scrapeable(detail: str) -> bool:
    """Check if goal detail is scrapeable"""
    return detail in EVENT_TYPES.get("Goal", {}).get("scrapeable_details", [])


def is_card_scrapeable(detail: str) -> bool:
    """Check if card detail is scrapeable"""
    return detail in EVENT_TYPES.get("Card", {}).get("scrapeable_details", [])


def get_event_type_for_id(event: dict) -> str:
    """
    Extract event type string for ID generation.
    
    Args:
        event: Event dict from API
        
    Returns:
        Event type string (e.g., "Goal", "Card")
    """
    return event.get("type", "Unknown")


# Constant for debounce stable count (default across all event types)
DEBOUNCE_STABLE_COUNT = 3
