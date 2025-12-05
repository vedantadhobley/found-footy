"""
Event Enhancement Utilities

Helper functions for building Twitter search queries and calculating score context.
These are used when enhancing events during the debounce process.
"""
from typing import Dict, Any


def build_twitter_search(event: dict, fixture: dict) -> str:
    """
    Build Twitter search string from event and fixture data.
    
    Format: "{PlayerLastName} {TeamName}"
    Example: "Salah Liverpool"
    
    Args:
        event: The goal event from API-Football
        fixture: The fixture data containing team information
    
    Returns:
        Search string for Twitter query
    """
    # Get player's last name
    player_name = event.get("player", {}).get("name", "Unknown")
    player_last_name = player_name.split()[-1] if player_name else "Unknown"
    
    # Get team name from fixture (more reliable than event.team)
    event_team_id = event.get("team", {}).get("id")
    home_team_id = fixture.get("teams", {}).get("home", {}).get("id")
    
    if event_team_id == home_team_id:
        team_name = fixture.get("teams", {}).get("home", {}).get("name", "")
    else:
        team_name = fixture.get("teams", {}).get("away", {}).get("name", "")
    
    return f"{player_last_name} {team_name}".strip()


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
