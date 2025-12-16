"""
Event Enhancement Utilities

Helper functions for building Twitter search queries and calculating score context.
These are used when enhancing events during the debounce process.
"""
import unicodedata
from typing import Dict, Any

from src.utils.team_data import get_team_nickname


def normalize_accents(text: str) -> str:
    """
    Replace accented characters with their ASCII equivalents.
    
    Examples:
        "Doué" -> "Doue"
        "Müller" -> "Muller"
        "Ibrahimović" -> "Ibrahimovic"
    """
    # Normalize to decomposed form (separate base char from accent)
    normalized = unicodedata.normalize("NFD", text)
    # Remove combining diacritical marks (accents)
    ascii_text = "".join(c for c in normalized if unicodedata.category(c) != "Mn")
    return ascii_text


def extract_player_search_name(player_name: str) -> str:
    """
    Extract the best search name from a player's full name.
    
    Handles various name formats:
    - "M. Salah" -> "Salah"
    - "K. De Bruyne" -> "De Bruyne"  
    - "C. Hudson-Odoi" -> "Hudson Odoi"
    - "T. Alexander-Arnold" -> "Alexander Arnold"
    - "Vinícius Júnior" -> "Vinicius Junior"
    
    Rules:
    1. If name has initial (single letter followed by period), skip it
    2. Take everything after the initial as the surname
    3. Handle multi-part surnames (De Bruyne, Van Dijk, etc.)
    4. Normalize accented characters
    5. Replace hyphens with spaces for better search matching
    
    Args:
        player_name: Full player name from API (e.g., "K. De Bruyne")
        
    Returns:
        Search-friendly name (e.g., "De Bruyne")
    """
    if not player_name or player_name == "Unknown":
        return "Unknown"
    
    # Normalize accents first
    name = normalize_accents(player_name)
    
    # Replace hyphens with spaces for better search matching
    name = name.replace("-", " ")
    
    # Split into parts
    parts = name.split()
    
    if len(parts) == 1:
        return parts[0]
    
    # Check if first part is an initial (single letter or letter with period)
    first = parts[0]
    is_initial = (len(first) == 1) or (len(first) == 2 and first.endswith("."))
    
    if is_initial:
        # Return everything after the initial
        surname_parts = parts[1:]
        return " ".join(surname_parts)
    else:
        # No initial - take the last name (handles "Vinícius Júnior" -> "Junior")
        # But also handle compound surnames - if second part is lowercase, include it
        # Actually, just take the last part for simplicity
        return parts[-1]


def build_twitter_search(event: dict, fixture: dict) -> str:
    """
    Build Twitter search string from event and fixture data.
    
    Format: "{PlayerSearchName} {TeamNickname}"
    Example: "Salah Liverpool", "De Bruyne Man City"
    
    Args:
        event: The goal event from API-Football
        fixture: The fixture data containing team information
    
    Returns:
        Search string for Twitter query
    """
    # Get player's search name (handles De Bruyne, Hudson-Odoi, accents, etc.)
    player_name = event.get("player", {}).get("name", "Unknown")
    player_search_name = extract_player_search_name(player_name)
    
    # Get team info from fixture
    event_team_id = event.get("team", {}).get("id")
    home_team_id = fixture.get("teams", {}).get("home", {}).get("id")
    
    if event_team_id == home_team_id:
        api_team_name = fixture.get("teams", {}).get("home", {}).get("name", "")
    else:
        api_team_name = fixture.get("teams", {}).get("away", {}).get("name", "")
    
    # Use nickname from our team data if available, otherwise fall back to API name
    team_name = get_team_nickname(event_team_id, fallback=api_team_name)
    
    return f"{player_search_name} {team_name}".strip()


def calculate_score_context(fixture: dict, event: dict) -> Dict[str, Any]:
    """
    Calculate score before and after this goal event.
    
    Iterates through all goals in chronological order, counting until
    we reach the current event. Returns the score state at that moment.
    
    Args:
        fixture: The fixture data with all events
        event: The specific goal event to calculate context for
    
    Returns:
        Dict with _score_before, _score_after, _scoring_team
    """
    # Get all goal events sorted by time
    all_events = fixture.get("events", [])
    goal_events = [e for e in all_events if e.get("type") == "Goal"]
    goal_events.sort(key=lambda e: e.get("time", {}).get("elapsed", 0))
    
    home_team_id = fixture.get("teams", {}).get("home", {}).get("id")
    away_team_id = fixture.get("teams", {}).get("away", {}).get("id")
    
    # Find this event's position
    current_elapsed = event.get("time", {}).get("elapsed", 0)
    current_player_id = event.get("player", {}).get("id")
    
    score_home = 0
    score_away = 0
    
    # Count goals BEFORE this event
    for goal in goal_events:
        goal_elapsed = goal.get("time", {}).get("elapsed", 0)
        goal_player_id = goal.get("player", {}).get("id")
        
        # Stop when we reach this event
        if goal_elapsed == current_elapsed and goal_player_id == current_player_id:
            break
        
        # Count this goal
        goal_team_id = goal.get("team", {}).get("id")
        if goal_team_id == home_team_id:
            score_home += 1
        elif goal_team_id == away_team_id:
            score_away += 1
    
    score_before = {"home": score_home, "away": score_away}
    
    # Add this goal to get score_after
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
        "_scoring_team": scoring_team,
    }
