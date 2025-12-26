"""Team data management - pure Python, no orchestration dependencies"""

# =============================================================================
# TRACKED TEAM IDs
# =============================================================================
# These are the teams we actively monitor for fixtures.
# Just IDs - no metadata. Team type (club/national) comes from API.
# Aliases for Twitter search come from RAG pipeline (Wikidata + Ollama).

# Top UEFA club teams (by coefficient)
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

def get_team_ids():
    """Get all tracked team IDs (clubs + national teams)"""
    return TOP_UEFA_IDS + TOP_FIFA_IDS


def get_uefa_team_ids():
    """Get tracked UEFA club team IDs"""
    return TOP_UEFA_IDS


def get_fifa_team_ids():
    """Get tracked FIFA national team IDs"""
    return TOP_FIFA_IDS


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
