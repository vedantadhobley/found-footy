"""
Event Enhancement Utilities

Helper functions for building Twitter search queries and calculating score context.
These are used when enhancing events during the debounce process.

NOTE: The Twitter search string built here is just a fallback/display string.
The actual Twitter search uses RAG-generated aliases from team_aliases collection,
which provides multiple aliases per team for better coverage.
"""
import unicodedata
from typing import Dict, Any

from src.data.models import EventFields


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


def extract_player_search_names(player_name: str) -> list[str]:
    """
    Extract searchable names from a player's full name.
    
    Returns multiple names when both first name and surname are good search terms.
    This allows Twitter search to use OR: "(Florian OR Wirtz) (LFC OR Liverpool)"
    
    Examples:
        "M. Salah" -> ["Salah"]
        "K. De Bruyne" -> ["Bruyne"]  
        "Florian Wirtz" -> ["Florian", "Wirtz"]  (both 5+ chars, include both!)
        "Vinícius Júnior" -> ["Vinicius", "Junior"]  (both 6+ chars)
        "T. Alexander-Arnold" -> ["Alexander", "Arnold"]  (hyphenated = both parts)
        "R. Diaz Belloli" -> ["Belloli", "Diaz"]  (both 4+ chars)
        "C. Ronaldo" -> ["Ronaldo"]  (initial skipped)
    
    Rules:
    1. Normalize accented characters
    2. Replace hyphens with spaces (for hyphenated names)
    3. Skip initials (single letter or letter+period)
    4. Include ALL name parts that are 4+ characters (searchable length)
    5. Order by length descending (longest/most distinctive first)
    6. For very common short names, still include if it's the only surname
    
    Args:
        player_name: Full player name from API (e.g., "Florian Wirtz")
        
    Returns:
        List of search-friendly names (e.g., ["Florian", "Wirtz"])
    """
    if not player_name or player_name == "Unknown":
        return ["Unknown"]
    
    # Normalize accents first
    name = normalize_accents(player_name)
    
    # Replace hyphens with spaces for better search matching
    name = name.replace("-", " ")
    
    # Split into parts
    parts = name.split()
    
    if len(parts) == 1:
        return [parts[0]]
    
    # Check if first part is an initial (single letter or letter with period)
    first = parts[0]
    is_initial = (len(first) == 1) or (len(first) == 2 and first.endswith("."))
    
    if is_initial:
        # Skip the initial, work with surname parts
        name_parts = parts[1:]
    else:
        # No initial - use all parts
        name_parts = parts
    
    if len(name_parts) == 1:
        return [name_parts[0]]
    
    # Filter to searchable names (4+ chars is a good threshold)
    # Shorter names like "De" or "Van" are not useful alone
    MIN_SEARCH_LENGTH = 4
    searchable = [p for p in name_parts if len(p) >= MIN_SEARCH_LENGTH]
    
    # If nothing passes the filter, fall back to the longest name
    if not searchable:
        longest = max(name_parts, key=len)
        return [longest]
    
    # Sort by length descending (most distinctive first)
    searchable.sort(key=len, reverse=True)
    
    # Limit to max 3 names to avoid overly complex queries
    return searchable[:3]


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
        "Sporting CP" -> "Sporting"
        "Atletico Madrid" -> "Atletico"
        "Aston Villa" -> "Villa" (tie, prefer last - the identifier)
        "Borussia Dortmund" -> "Dortmund" (tie, prefer last - the city)
        "Real Madrid" -> "Madrid"
        "Manchester City" -> "Manchester"
        "Newcastle United" -> "Newcastle"
        "West Ham United" -> "Ham" (after filtering United)
        "Crystal Palace" -> "Palace" (tie, prefer last)
        
    Rules:
    1. Normalize accents
    2. Remove special characters (& etc.)
    3. Replace hyphens with spaces
    4. Filter out very short words (1-2 chars like FC, RB, CP, AC, etc.)
    5. Filter out truly generic suffixes (United, City, Town, Wanderers, Rovers, Albion)
       - These appear in MANY team names and aren't distinctive
       - But keep Sporting, Atletico, Real, Villa, etc. - these ARE the team identity
    6. Use the longest remaining word
    7. On ties, prefer the LAST word (usually city name, more distinctive)
    
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
    
    # Filter out very short words (abbreviations like FC, RB, CP, AC, SC, 04, 05, etc.)
    # AND truly generic suffixes that appear in many team names
    # Note: We keep Sporting, Atletico, Real, Villa, etc. - these ARE the team identity!
    generic_suffixes = {"united", "city", "town", "wanderers", "rovers", "albion", "county", "athletic"}
    
    meaningful_words = [
        w for w in words 
        if len(w) > 2 and w.lower() not in generic_suffixes
    ]
    
    # If all words were filtered, fall back to original (filtering short words only)
    if not meaningful_words:
        meaningful_words = [w for w in words if len(w) > 2]
    
    # If still empty, use original words
    if not meaningful_words:
        meaningful_words = words
    
    # If only one meaningful word, use it
    if len(meaningful_words) == 1:
        return meaningful_words[0]
    
    # Sort by length descending
    sorted_by_len = sorted(meaningful_words, key=len, reverse=True)
    longest = sorted_by_len[0]
    second = sorted_by_len[1] if len(sorted_by_len) > 1 else ""
    
    # If words are very similar length (diff <= 1), keep both together
    # This handles "West Ham" (4,3), "Crystal Palace" (7,6)
    # But NOT "Atletico Madrid" (8,6) or "Borussia Dortmund" (8,8)
    length_diff = len(longest) - len(second)
    
    if length_diff <= 1 and len(longest) <= 7:
        # Short words that are similar - keep together
        # e.g., West Ham (4,3), Crystal Palace (7,6)
        return " ".join(meaningful_words[:2])
    elif length_diff == 0:
        # Exact tie - prefer LAST word (Borussia Dortmund -> Dortmund, Eintracht Frankfurt -> Frankfurt)
        return meaningful_words[-1]
    else:
        # Clear winner - use just the longest
        return longest


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
    player_search_names = extract_player_search_names(player_name)
    player_search_name = player_search_names[0] if player_search_names else "Unknown"
    
    # Get team info from fixture
    event_team_id = event.get("team", {}).get("id")
    home_team_id = fixture.get("teams", {}).get("home", {}).get("id")
    
    if event_team_id == home_team_id:
        api_team_name = fixture.get("teams", {}).get("home", {}).get("name", "")
    else:
        api_team_name = fixture.get("teams", {}).get("away", {}).get("name", "")
    
    # Use nickname from our team data if available
    # Otherwise, extract the best search term from the API name
    # NOTE: This is just for display/debugging. Actual Twitter search uses RAG aliases.
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
        Dict with _score_after, _scoring_team
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
        EventFields.SCORE_AFTER: score_after,
        EventFields.SCORING_TEAM: scoring_team,
    }
