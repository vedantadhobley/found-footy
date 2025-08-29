from found_footy.storage.mongo_store import FootyMongoStore
import requests
from datetime import date, datetime
import json

# Initialize MongoDB store
store = FootyMongoStore()

# API configuration
BASE_URL = "https://api-football-v1.p.rapidapi.com/v3"
HEADERS = {
    "x-rapidapi-key": "3815c39f56msh68991ec604f7be3p1cb7c4jsnf78a6cb47415",
    "x-rapidapi-host": "api-football-v1.p.rapidapi.com"
}

# League configurations
AVAILABLE_LEAGUES = {
    # Domestic Leagues
    39: {"name": "Premier League", "type": "domestic", "country": "England"},
    # 140: {"name": "La Liga", "type": "domestic", "country": "Spain"},
    # 78: {"name": "Bundesliga", "type": "domestic", "country": "Germany"},
    # 61: {"name": "Ligue 1", "type": "domestic", "country": "France"},
    # 135: {"name": "Serie A", "type": "domestic", "country": "Italy"},
    
    # European Competitions
    2: {"name": "UEFA Champions League", "type": "international", "country": "Europe"},
    # 3: {"name": "UEFA Europa League", "type": "international", "country": "Europe"},
    # 848: {"name": "UEFA Europa Conference League", "type": "international", "country": "Europe"},
    
    # English Cups
    48: {"name": "FA Cup", "type": "cup", "country": "England"},
    # 45: {"name": "EFL Cup", "type": "cup", "country": "England"},
    
    # Spanish Cups
    # 143: {"name": "Copa del Rey", "type": "cup", "country": "Spain"},
    # 556: {"name": "Supercopa de España", "type": "cup", "country": "Spain"},
    
    # German Cups
    # 81: {"name": "DFB Pokal", "type": "cup", "country": "Germany"},
    
    # French Cups
    # 66: {"name": "Coupe de France", "type": "cup", "country": "France"},
    
    # Italian Cups
    # 137: {"name": "Coppa Italia", "type": "cup", "country": "Italy"},
    
    # International
    # 4: {"name": "UEFA Nations League", "type": "international", "country": "Europe"},
    1: {"name": "World Cup", "type": "international", "country": "World"},
}

# ✅ CORE API FUNCTIONS - These fetch from external API
def get_fixtures_by_leagues(league_ids, query_date=date.today()):
    """Get fixtures by league IDs directly from API"""
    print(f"🔍 Fetching fixtures for leagues {league_ids} on {query_date}")
    
    # Fetch fixtures from API filtered by date
    fixture_url = f"{BASE_URL}/fixtures?date={query_date}"
    response = requests.get(fixture_url, headers=HEADERS)
    fixtures = response.json().get("response", [])

    selected_fixtures = []
    for fixture in fixtures:
        league_id = fixture["league"]["id"]
        
        # Filter by league ID directly
        if league_id in league_ids:
            selected_fixtures.append({
                "id": fixture["fixture"]["id"],
                "home": fixture["teams"]["home"]["name"],
                "away": fixture["teams"]["away"]["name"],
                "league": fixture["league"]["name"],
                "league_id": league_id,
                "time": fixture["fixture"]["date"]
            })
    
    print(f"✅ Found {len(selected_fixtures)} fixtures in specified leagues")
    return selected_fixtures

def get_fixture_details(fixture_ids):
    """Get fixture details from API"""
    url = f"{BASE_URL}/fixtures"
    querystring = {"ids": str(fixture_ids)}
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    return response.json().get("response", [])

def get_fixture_events(fixture_id):
    """Get fixture events from API"""
    url = f"{BASE_URL}/fixtures/events"
    querystring = {"fixture": str(fixture_id)}
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    return response.json().get("response", [])

# ✅ REQUIRED: These are used by flows and need to exist here
def store_fixture_result(fixture_data):
    """Store fixture result in MongoDB"""
    return store.store_fixture(fixture_data)

def store_fixture_events(fixture_id, events_data):
    """Store fixture events in MongoDB"""
    return store.store_fixture_events(fixture_id, events_data)

def populate_league_metadata(reset_first=True):
    """Populate league metadata in MongoDB with optional reset"""
    if reset_first:
        print("🔄 Resetting MongoDB first...")
        store.reset_database()
    
    print("🏆 Populating league metadata...")
    
    for league_id, league_info in AVAILABLE_LEAGUES.items():
        league_data = {
            "league_id": league_id,
            "league_name": league_info["name"],
            "league_type": league_info["type"],
            "country": league_info["country"],
            "season": 2025
        }
        store.store_league_metadata(league_data)
    
    print(f"✅ Populated {len(AVAILABLE_LEAGUES)} leagues in metadata")

# ✅ HELPER FUNCTIONS - These provide useful abstractions
def get_available_leagues():
    """Get all available leagues - returns constant dict"""
    return AVAILABLE_LEAGUES

def get_leagues_by_type(league_type):
    """Get leagues filtered by type (domestic, cup, international)"""
    return {
        league_id: league_info 
        for league_id, league_info in AVAILABLE_LEAGUES.items() 
        if league_info["type"] == league_type
    }

def parse_league_ids_parameter(league_ids_param):
    """Parse league IDs from various input formats - used by flows"""
    if isinstance(league_ids_param, str):
        try:
            # Try parsing as JSON array
            league_ids = json.loads(league_ids_param)
        except json.JSONDecodeError:
            # Try parsing as comma-separated string
            league_ids = [int(x.strip()) for x in league_ids_param.split(",")]
    elif isinstance(league_ids_param, list):
        league_ids = [int(x) for x in league_ids_param]
    else:
        league_ids = [int(league_ids_param)]
    
    return league_ids

# ✅ REMOVED: Unnecessary one-liner wrappers
# - reset_mongodb() → use store.reset_database() directly
# - get_completed_fixtures_today() → use store.get_completed_fixtures() directly  
# - search_fixtures_by_team() → use store.search_fixtures_by_team() directly
# - get_database_stats() → use store.get_stats() directly

# ✅ DEPRECATED NOTICE
def get_fixtures_by_date(query_date=date.today(), table="teams_2526"):
    """DEPRECATED: Use get_fixtures_by_leagues instead"""
    print("⚠️ WARNING: get_fixtures_by_date is deprecated. Use get_fixtures_by_leagues instead.")
    # Default to currently active leagues for backward compatibility
    default_leagues = [39, 2, 48, 1]  # ✅ UPDATED: Only active leagues
    return get_fixtures_by_leagues(default_leagues, query_date)