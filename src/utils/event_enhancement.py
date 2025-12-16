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
    
    Uses the longest word from the surname, which is typically the most
    distinctive and searchable part of the name.
    
    Examples:
        "M. Salah" -> "Salah"
        "K. De Bruyne" -> "Bruyne"  
        "C. Hudson-Odoi" -> "Hudson"  (both 6 chars, prefer last)
        "T. Alexander-Arnold" -> "Alexander"  (longer than Arnold)
        "R. Diaz Belloli" -> "Belloli"  (longer than Diaz)
        "Vinícius Júnior" -> "Vinicius"  (longer than Junior)
    
    Rules:
    1. Normalize accented characters
    2. Replace hyphens with spaces
    3. Skip initial if present (single letter or letter+period)
    4. Use the longest word from remaining parts
    5. On ties, prefer the LAST word (typically the family name)
    
    Args:
        player_name: Full player name from API (e.g., "K. De Bruyne")
        
    Returns:
        Search-friendly name (e.g., "Bruyne")
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
        # Skip the initial, work with surname parts
        surname_parts = parts[1:]
    else:
        # No initial - use all parts
        surname_parts = parts
    
    if len(surname_parts) == 1:
        return surname_parts[0]
    
    # Find the longest word
    # On ties, prefer the LAST word (typically the actual family name)
    # We reverse the list so that when max() finds equal lengths, it gets the last one
    longest = max(reversed(surname_parts), key=len)
    
    return longest


def extract_team_search_name(team_name: str) -> str:
    """
    Extract the best search term from a team's full name.
    
    For untracked teams, we need to find the most distinctive/searchable word.
    Strategy: Use the longest word (usually the most specific/unique identifier).
    
    Examples:
        "Borussia Mönchengladbach" -> "Monchengladbach"
        "Brighton & Hove Albion" -> "Brighton"
        "Wolverhampton Wanderers" -> "Wolverhampton"
        "RB Leipzig" -> "Leipzig"
        "West Ham United" -> "West Ham"  (special case: keep "West Ham" together)
        "Crystal Palace" -> "Crystal Palace" (both words similar length, keep both)
        "Nottingham Forest" -> "Nottingham"
        "Newcastle United" -> "Newcastle"
        
    Rules:
    1. Normalize accents
    2. Remove special characters (& etc.)
    3. Replace hyphens with spaces
    4. Filter out common suffixes (FC, United, City, Wanderers, Rovers, etc.)
    5. Use the longest remaining word
    6. If multiple words of similar length, prefer keeping them together
    
    Args:
        team_name: Full team name from API
        
    Returns:
        Search-friendly team identifier
    """
    if not team_name:
        return ""
    
    # Normalize accents
    name = normalize_accents(team_name)
    
    # Remove special characters but keep spaces
    name = name.replace("&", " ").replace("-", " ")
    
    # Split into words
    words = name.split()
    
    # Common suffixes/prefixes to filter out (not distinctive)
    common_words = {
        "fc", "cf", "sc", "ac", "as", "ss", "rb", "vfb", "fsv", "sv", "bv",
        "united", "city", "town", "wanderers", "rovers", "athletic", "albion",
        "hotspur", "villa", "county", "olympic", "olympique", "sporting",
        "real", "atletico", "deportivo", "racing", "dynamo", "lokomotiv",
        "borussia",  # Generic prefix for German clubs
    }
    
    # Filter out common words
    distinctive_words = [w for w in words if w.lower() not in common_words]
    
    # If all words were filtered, use original words
    if not distinctive_words:
        distinctive_words = words
    
    # If only one distinctive word, use it
    if len(distinctive_words) == 1:
        return distinctive_words[0]
    
    # Find the longest word
    longest = max(distinctive_words, key=len)
    
    # If longest is significantly longer (3+ chars), use just that
    second_longest = sorted(distinctive_words, key=len, reverse=True)[1] if len(distinctive_words) > 1 else ""
    
    if len(longest) >= len(second_longest) + 3:
        return longest
    
    # Otherwise, words are similar length - keep them together
    # But limit to first 2 distinctive words to avoid overly long queries
    return " ".join(distinctive_words[:2])


def build_twitter_search(event: dict, fixture: dict) -> str:
    """
    Build Twitter search string from event and fixture data.
    
    Format: "{PlayerSearchName} {TeamNickname}"
    Example: "Salah Liverpool", "De Bruyne Man City"
    
    For tracked teams, uses our curated nickname.
    For untracked teams, extracts the most distinctive word from the API name.
    
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
    
    # Use nickname from our team data if available
    # Otherwise, extract the best search term from the API name
    team_name = get_team_nickname(event_team_id, fallback=None)
    if team_name is None:
        team_name = extract_team_search_name(api_team_name)
    
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
