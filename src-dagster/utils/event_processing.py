"""Event processing utilities - ID generation, hashing, and sequential assignment

DEPRECATED FILE - NOT USED IN CURRENT ARCHITECTURE
===================================================

This entire file is deprecated. Current architecture uses:
- mongo_store._generate_event_id() for event ID generation
  Format: {fixture_id}_{player_id}_{elapsed}_{type}_{detail}
- debounce_job.generate_event_hash() for hash generation
- Event filtering happens in store_live_fixture() with should_track_event()

This file is kept for reference only. None of these functions are imported or used.
If you need event processing utilities, look in:
- src/data/mongo_store.py (_generate_event_id)
- src/jobs/debounce/debounce_job.py (generate_event_hash, build_twitter_search)
- src/utils/event_config.py (should_track_event)

==================================================
"""
import hashlib
import json
from typing import Dict, List


# DEPRECATED: Use mongo_store._generate_event_id() instead
# Current architecture uses format: {fixture_id}_{player_id}_{elapsed}_{type}_{detail}
# This function uses old format: {fixture_id}_{player_id}_{event_type}_{sequence}
def generate_event_id(fixture_id: int, player_id: int, event_type: str, sequence: int) -> str:
    """
    DEPRECATED: Generate sequential event ID (old format).
    
    Current architecture uses mongo_store._generate_event_id()
    Format: {fixture_id}_{player_id}_{elapsed}_{type}_{detail}
    
    This function kept only because it's used by assign_sequential_ids()
    in this deprecated file.
    """
    return f"{fixture_id}_{player_id}_{event_type}_{sequence}"


# DEPRECATED: Use with generate_event_id above
def parse_event_id(event_id: str) -> dict:
    """
    DEPRECATED: Parse event ID back into components (old format).
    
    This function kept only because it's used by assign_sequential_ids()
    in this deprecated file.
    """
    parts = event_id.split('_')
    if len(parts) != 4:
        raise ValueError(f"Invalid event ID format: {event_id}")
    
    return {
        "fixture_id": int(parts[0]),
        "player_id": int(parts[1]),
        "event_type": parts[2],
        "sequence": int(parts[3])
    }


def generate_search_hash(event: dict, debounce_fields: list) -> str:
    """
    Generate hash from event fields that affect Twitter search.
    
    Only hashes the fields that matter for Twitter search to avoid
    unnecessary debounce resets from irrelevant metadata changes.
    
    Args:
        event: Event dict from API
        debounce_fields: List of field paths like ["player.name", "team.name"]
        
    Returns:
        16-character hash string
        
    Example:
        >>> event = {
        ...     "player": {"id": 306, "name": "D. Szoboszlai"},
        ...     "team": {"id": 40, "name": "Liverpool"},
        ...     "time": {"elapsed": 67}
        ... }
        >>> hash = generate_search_hash(event, ["player.name", "team.name"])
        >>> len(hash)
        16
    """
    # Extract only the relevant fields
    search_data = {}
    for field_path in debounce_fields:
        value = _get_nested_field(event, field_path)
        search_data[field_path] = value
    
    # Create stable hash
    content = json.dumps(search_data, sort_keys=True)
    return hashlib.sha256(content.encode()).hexdigest()[:16]


def _get_nested_field(data: dict, field_path: str):
    """
    Get nested field value using dot notation.
    
    Args:
        data: Dict to extract from
        field_path: Dot-separated path like "player.name"
        
    Returns:
        Field value or empty string if not found
    """
    parts = field_path.split('.')
    value = data
    
    for part in parts:
        if isinstance(value, dict):
            value = value.get(part, '')
        else:
            return ''
    
    return value


def build_twitter_search_query(event: dict, event_type: str) -> str:
    """
    Build Twitter search query from event data.
    
    Args:
        event: Event dict from API
        event_type: Type of event (Goal, Card, etc)
        
    Returns:
        Search query string for Twitter
        
    Examples:
        Goal: "Szoboszlai Liverpool"
        Red card: "Fernandes Manchester United red card"
    """
    player_name = event.get("player", {}).get("name", "")
    team_name = event.get("team", {}).get("name", "")
    
    # Use last name only for search (better results)
    if player_name:
        name_parts = player_name.split()
        player_name = name_parts[-1] if name_parts else player_name
    
    # Base query
    query = f"{player_name} {team_name}".strip()
    
    # Add event-specific suffix
    if event_type == "Card":
        detail = event.get("detail", "")
        if "Red" in detail:
            query += " red card"
    
    return query


def assign_sequential_ids(fixture_id: int, events: List[dict], existing_event_ids: Dict[str, dict]) -> Dict[str, dict]:
    """
    Assign sequential IDs to events, respecting existing IDs from database.
    
    This ensures stable IDs across polls - events that already exist keep their IDs,
    new events get the next available sequence number.
    
    Args:
        fixture_id: Fixture ID
        events: List of event dicts from API
        existing_event_ids: Dict mapping search_hash -> existing event_id
        
    Returns:
        Dict mapping event_id -> event_data
        
    Example:
        >>> events = [
        ...     {"type": "Goal", "player": {"id": 234}, ...},  # Existing
        ...     {"type": "Goal", "player": {"id": 234}, ...}   # New
        ... ]
        >>> existing = {"hash1": "5000_234_Goal_1"}
        >>> result = assign_sequential_ids(5000, events, existing)
        >>> list(result.keys())
        ['5000_234_Goal_1', '5000_234_Goal_2']
    """
    from .event_config import get_debounce_fields
    
    # Track max sequence per player+type
    max_sequences = {}  # Key: "player_id_event_type" -> max sequence seen
    
    # Parse existing IDs to get max sequences
    for event_id in existing_event_ids.values():
        try:
            parsed = parse_event_id(event_id)
            counter_key = f"{parsed['player_id']}_{parsed['event_type']}"
            max_sequences[counter_key] = max(
                max_sequences.get(counter_key, 0),
                parsed['sequence']
            )
        except ValueError:
            continue
    
    # Assign IDs to events
    event_id_map = {}
    
    for event in events:
        player_id = event.get("player", {}).get("id", 0)
        event_type = event.get("type")
        
        if not player_id or not event_type:
            continue
        
        # Calculate search hash
        debounce_fields = get_debounce_fields(event_type)
        search_hash = generate_search_hash(event, debounce_fields)
        
        # Check if this event already exists
        if search_hash in existing_event_ids:
            # Use existing ID
            event_id = existing_event_ids[search_hash]
        else:
            # Assign new sequential ID
            counter_key = f"{player_id}_{event_type}"
            next_seq = max_sequences.get(counter_key, 0) + 1
            max_sequences[counter_key] = next_seq
            
            event_id = generate_event_id(fixture_id, player_id, event_type, next_seq)
        
        event_id_map[event_id] = event
    
    return event_id_map
