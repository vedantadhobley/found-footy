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

# ✅ CHANGED: Top 25 UEFA ranked teams for 2026 season (replacing leagues)
TOP_25_TEAMS_2026 = {
    541: {"name": "Real Madrid", "country": "Spain"},
    81: {"name": "Bayern Munich", "country": "Germany"},
    110: {"name": "Inter Milan", "country": "Italy"},
    50: {"name": "Manchester City", "country": "England"},
    42: {"name": "Liverpool", "country": "England"},
    85: {"name": "Paris Saint-Germain", "country": "France"},
    98: {"name": "Bayer Leverkusen", "country": "Germany"},
    83: {"name": "Borussia Dortmund", "country": "Germany"},
    529: {"name": "Barcelona", "country": "Spain"},
    211: {"name": "Benfica", "country": "Portugal"},
    530: {"name": "Atletico Madrid", "country": "Spain"},
    487: {"name": "Roma", "country": "Italy"},
    49: {"name": "Chelsea", "country": "England"},
    40: {"name": "Arsenal", "country": "England"},
    91: {"name": "Eintracht Frankfurt", "country": "Germany"},
    33: {"name": "Manchester United", "country": "England"},
    499: {"name": "Atalanta", "country": "Italy"},
    82: {"name": "Feyenoord", "country": "Netherlands"},
    48: {"name": "West Ham United", "country": "England"},
    121: {"name": "Club Brugge", "country": "Belgium"},
    489: {"name": "AC Milan", "country": "Italy"},
    79: {"name": "PSV Eindhoven", "country": "Netherlands"},
    502: {"name": "Fiorentina", "country": "Italy"},
    228: {"name": "Sporting CP", "country": "Portugal"},
    47: {"name": "Tottenham Hotspur", "country": "England"}
}

# ✅ CORE API FUNCTIONS - Updated for team-based approach
def get_fixtures_by_teams(team_ids, query_date=date.today()):
    """Get fixtures by team IDs directly from API"""
    print(f"🔍 Fetching fixtures for teams {team_ids} on {query_date}")
    
    # Fetch all fixtures from API for the date
    fixture_url = f"{BASE_URL}/fixtures?date={query_date}"
    response = requests.get(fixture_url, headers=HEADERS)
    fixtures = response.json().get("response", [])

    selected_fixtures = []
    for fixture in fixtures:
        home_team_id = fixture["teams"]["home"]["id"]
        away_team_id = fixture["teams"]["away"]["id"]
        
        # Check if either team is in our target list
        if home_team_id in team_ids or away_team_id in team_ids:
            selected_fixtures.append({
                "id": fixture["fixture"]["id"],
                "home": fixture["teams"]["home"]["name"],
                "home_id": home_team_id,
                "away": fixture["teams"]["away"]["name"],
                "away_id": away_team_id,
                "league": fixture["league"]["name"],
                "league_id": fixture["league"]["id"],
                "time": fixture["fixture"]["date"]
            })
    
    print(f"✅ Found {len(selected_fixtures)} fixtures involving target teams")
    return selected_fixtures

def get_fixtures_by_date(query_date=date.today()):
    """Get fixtures by date for top 25 teams - REIMPLEMENTED"""
    print(f"🔍 Fetching fixtures for top 25 UEFA teams on {query_date}")
    
    # Use all top 25 team IDs
    top_25_team_ids = list(TOP_25_TEAMS_2026.keys())
    
    return get_fixtures_by_teams(top_25_team_ids, query_date)

# ✅ BACKWARD COMPATIBILITY: Keep league function but use teams internally
def get_fixtures_by_leagues(league_ids, query_date=date.today()):
    """DEPRECATED: Use get_fixtures_by_teams or get_fixtures_by_date instead"""
    print("⚠️ WARNING: get_fixtures_by_leagues is deprecated. Using team-based approach instead.")
    # Default to top 25 teams for backward compatibility
    return get_fixtures_by_date(query_date)

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

def populate_team_metadata(reset_first=True):
    """Populate team metadata in MongoDB with optional reset"""
    if reset_first:
        print("🔄 Resetting MongoDB first...")
        store.reset_database()
    
    print("⚽ Populating top 25 UEFA team metadata...")
    
    for team_id, team_info in TOP_25_TEAMS_2026.items():
        team_data = {
            "team_id": team_id,
            "team_name": team_info["name"],
            "country": team_info["country"],
            "season": 2026,
            "uefa_ranking": list(TOP_25_TEAMS_2026.keys()).index(team_id) + 1  # 1-25 ranking
        }
        store.store_team_metadata(team_data)
    
    print(f"✅ Populated {len(TOP_25_TEAMS_2026)} teams in metadata")

# ✅ UPDATED: Helper functions for teams
def get_available_teams():
    """Get all available top 25 teams - returns constant dict"""
    return TOP_25_TEAMS_2026

def get_teams_by_country(country):
    """Get teams filtered by country"""
    return {
        team_id: team_info 
        for team_id, team_info in TOP_25_TEAMS_2026.items() 
        if team_info["country"].lower() == country.lower()
    }

def parse_team_ids_parameter(team_ids_param):
    """Parse team IDs from various input formats - used by flows"""
    if isinstance(team_ids_param, str):
        try:
            # Try parsing as JSON array
            team_ids = json.loads(team_ids_param)
        except json.JSONDecodeError:
            # Try parsing as comma-separated string
            team_ids = [int(x.strip()) for x in team_ids_param.split(",")]
    elif isinstance(team_ids_param, list):
        team_ids = [int(x) for x in team_ids_param]
    else:
        team_ids = [int(team_ids_param)]
    
    return team_ids

# ✅ BACKWARD COMPATIBILITY: Keep league functions but redirect to teams
def get_available_leagues():
    """DEPRECATED: Use get_available_teams instead"""
    print("⚠️ WARNING: get_available_leagues is deprecated. Use get_available_teams instead.")
    return TOP_25_TEAMS_2026

def parse_league_ids_parameter(league_ids_param):
    """DEPRECATED: Use parse_team_ids_parameter instead"""
    print("⚠️ WARNING: parse_league_ids_parameter is deprecated. Use parse_team_ids_parameter instead.")
    return parse_team_ids_parameter(league_ids_param)

# ✅ DEPRECATED: Keep for backward compatibility but use team approach
def populate_league_metadata(reset_first=True):
    """DEPRECATED: Use populate_team_metadata instead"""
    print("⚠️ WARNING: populate_league_metadata is deprecated. Using populate_team_metadata instead.")
    return populate_team_metadata(reset_first)