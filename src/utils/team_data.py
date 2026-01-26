"""Team data management - pure Python, no orchestration dependencies"""
import logging
from datetime import datetime, timezone, timedelta

logger = logging.getLogger(__name__)

# =============================================================================
# TOP 5 EUROPEAN LEAGUES
# =============================================================================
# The "top flight" in soccer - these leagues' teams are automatically tracked.
# Team rosters change each season (promotion/relegation), so we fetch dynamically.

TOP_5_LEAGUES = {
    39: "Premier League",      # England
    140: "La Liga",            # Spain
    78: "Bundesliga",          # Germany
    135: "Serie A",            # Italy
    61: "Ligue 1",             # France
}

# Cache settings for top-flight teams
TOP_FLIGHT_CACHE_HOURS = 24  # Refresh team list daily

# =============================================================================
# LEGACY TRACKED TEAM IDs (for reference / fallback)
# =============================================================================
# These were the original manually-tracked teams before dynamic top-5 leagues.
# Kept for backward compatibility if API fails.

# Top UEFA club teams (by coefficient) - LEGACY FALLBACK
TOP_UEFA_IDS = [
    541,   # Real Madrid
    157,   # Bayern München
    50,    # Manchester City
    85,    # Paris Saint Germain
    529,   # Barcelona
    40,    # Liverpool
    530,   # Atletico Madrid
    42,    # Arsenal
    33,    # Manchester United
    165,   # Borussia Dortmund
    49,    # Chelsea
    496,   # Juventus
    497,   # AS Roma
    505,   # Inter
    47,    # Tottenham
]

# Top FIFA national teams (by ranking)
# NOTE: National teams are tracked separately - you decide how to handle these
TOP_FIFA_IDS = [
    9,     # Spain
    26,    # Argentina
    2,     # France
    10,    # England
    6,     # Brazil
    27,    # Portugal
    1118,  # Netherlands
    1,     # Belgium
    25,    # Germany
    3,     # Croatia
    31,    # Morocco
    768,   # Italy
    8,     # Colombia
    2384,  # USA
    16,    # Mexico
]


# =============================================================================
# DYNAMIC TOP-FLIGHT TEAM FUNCTIONS
# =============================================================================

def get_top_flight_team_ids(force_refresh: bool = False) -> list[int]:
    """
    Get all team IDs from the top 5 European leagues for the current season.
    
    Fetches teams dynamically from API-Football and caches in MongoDB.
    Cache is refreshed if older than TOP_FLIGHT_CACHE_HOURS.
    
    Args:
        force_refresh: Force API refresh even if cache is valid
        
    Returns:
        List of team IDs from all top 5 leagues
    """
    from src.data.mongo_store import FootyMongoStore
    from src.api.api_client import get_current_season, get_teams_for_league
    
    store = FootyMongoStore()
    
    # Check cache first
    if not force_refresh:
        cached = store.get_top_flight_cache()
        if cached:
            cached_at = cached.get("cached_at")
            if cached_at:
                age = datetime.now(timezone.utc) - cached_at
                if age < timedelta(hours=TOP_FLIGHT_CACHE_HOURS):
                    team_ids = cached.get("team_ids", [])
                    logger.info(f"Using cached top-flight teams ({len(team_ids)} teams, cached {age.total_seconds()/3600:.1f}h ago)")
                    return team_ids
    
    # Fetch fresh data from API
    logger.info("Fetching top-flight teams from API-Football...")
    all_team_ids = set()
    season = None
    
    for league_id, league_name in TOP_5_LEAGUES.items():
        # Get current season (same for all top 5, so reuse)
        if season is None:
            season = get_current_season(league_id)
            if season is None:
                logger.error(f"Could not determine current season for {league_name}")
                continue
            logger.info(f"Current season: {season}")
        
        # Get teams for this league
        team_ids = get_teams_for_league(league_id, season)
        if team_ids:
            logger.info(f"  {league_name}: {len(team_ids)} teams")
            all_team_ids.update(team_ids)
        else:
            logger.warning(f"  {league_name}: No teams found!")
    
    team_ids_list = list(all_team_ids)
    
    if team_ids_list:
        # Cache the results
        store.save_top_flight_cache(team_ids_list, season)
        logger.info(f"Cached {len(team_ids_list)} top-flight teams for season {season}")
    else:
        # Fallback to legacy list if API failed completely
        logger.warning("API fetch failed, using legacy TOP_UEFA_IDS as fallback")
        team_ids_list = TOP_UEFA_IDS
    
    return team_ids_list


def get_team_ids():
    """
    Get all tracked team IDs (top-flight clubs + national teams).
    
    This is the main entry point for filtering fixtures.
    """
    # Get dynamic top-flight teams
    club_ids = get_top_flight_team_ids()
    
    # Add national teams (static for now)
    all_ids = list(set(club_ids + TOP_FIFA_IDS))
    
    return all_ids


def get_uefa_team_ids():
    """
    Get tracked UEFA club team IDs.
    
    Now returns dynamic top-flight teams instead of static list.
    """
    return get_top_flight_team_ids()


def get_fifa_team_ids():
    """Get tracked FIFA national team IDs (static list)"""
    return TOP_FIFA_IDS


def get_legacy_uefa_ids():
    """Get the original static UEFA club list (for fallback/testing)"""
    return TOP_UEFA_IDS


def is_team_tracked(team_id: int) -> bool:
    """Check if team ID is in our tracking list"""
    return team_id in get_team_ids()


def is_national_team(team_id: int) -> bool | None:
    """
    Determine if a team is a national team using cached data or API.
    
    Priority:
    1. Check MongoDB cache (team_aliases collection has 'national' boolean)
    2. Call API-Football to get team.national
    
    Args:
        team_id: API-Football team ID
        
    Returns:
        True if national team, False if club, None if lookup failed
    """
    from src.data.mongo_store import FootyMongoStore
    from src.data.models import TeamAliasFields
    
    store = FootyMongoStore()
    
    # 1. Check cache first
    cached = store.get_team_alias(team_id)
    if cached:
        # New docs have 'national' boolean
        if TeamAliasFields.NATIONAL in cached:
            return cached[TeamAliasFields.NATIONAL]
        # Legacy docs only have 'team_type' string
        if TeamAliasFields.TEAM_TYPE in cached:
            return cached[TeamAliasFields.TEAM_TYPE] == "national"
    
    # 2. API lookup
    try:
        from src.api.api_client import is_national_team as api_is_national_team
        result = api_is_national_team(team_id)
        return result
    except Exception:
        return None


def get_team_type(team_id: int) -> str:
    """
    Get team type as string ("national" or "club").
    
    Wrapper around is_national_team() for backward compatibility.
    
    Args:
        team_id: API-Football team ID
        
    Returns:
        "national" or "club"
    """
    result = is_national_team(team_id)
    if result is True:
        return "national"
    # Default to "club" if False or None (most teams are clubs)
    return "club"
