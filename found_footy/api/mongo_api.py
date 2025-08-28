from found_footy.storage.mongo_store import FootyMongoStore
import requests
from datetime import date, datetime

# Initialize MongoDB store
store = FootyMongoStore()

# Keep your existing API configuration
BASE_URL = "https://api-football-v1.p.rapidapi.com/v3"
HEADERS = {
    "x-rapidapi-key": "3815c39f56msh68991ec604f7be3p1cb7c4jsnf78a6cb47415",
    "x-rapidapi-host": "api-football-v1.p.rapidapi.com"
}

leagues = [
    (39, "Premier League"),
    (140, "La Liga"),
    (78, "Bundesliga"),
    (61, "Ligue 1"),
    (135, "Serie A")
]

def get_fixtures_by_date(query_date=date.today(), table="teams_2526"):
    """Get fixtures by date using MongoDB"""
    # Extract season from table name (e.g., "teams_2526" -> 2025)
    season = 2000 + int(table.split('_')[1][:2])
    
    # Get team IDs from MongoDB
    team_ids = store.get_team_ids(season)
    
    # Fetch fixtures from API
    fixture_url = f"{BASE_URL}/fixtures?date={query_date}"
    response = requests.get(fixture_url, headers=HEADERS)
    fixtures = response.json().get("response", [])

    selected_fixtures = []
    for fixture in fixtures:
        home_id = fixture["teams"]["home"]["id"]
        away_id = fixture["teams"]["away"]["id"]
        if home_id in team_ids or away_id in team_ids:
            selected_fixtures.append({
                "id": fixture["fixture"]["id"],
                "home": fixture["teams"]["home"]["name"],
                "away": fixture["teams"]["away"]["name"],
                "league": fixture["league"]["name"],
                "time": fixture["fixture"]["date"]
            })
    return selected_fixtures

def get_fixture_details(fixture_ids):
    """Get fixture details from API (unchanged)"""
    url = f"{BASE_URL}/fixtures"
    querystring = {"ids": str(fixture_ids)}
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    return response.json().get("response", [])

def get_fixture_events(fixture_id):
    """Get fixture events from API (unchanged)"""
    url = f"{BASE_URL}/fixtures/events"
    querystring = {"fixture": str(fixture_id)}
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    return response.json().get("response", [])

def store_fixture_result(fixture_data, table_name=None):
    """Store fixture result in MongoDB (table_name ignored)"""
    return store.store_fixture(fixture_data)

def store_fixture_events(fixture_id, events_data, table_name=None):
    """Store fixture events in MongoDB (table_name ignored)"""
    return store.store_fixture_events(fixture_id, events_data)

def get_teams_for_league(league_id, season):
    """Get teams for league from API (unchanged)"""
    url = f"{BASE_URL}/teams?league={league_id}&season={season}"
    resp = requests.get(url, headers=HEADERS)
    data = resp.json()
    teams = []
    for team in data.get("response", []):
        teams.append({
            "team_id": team["team"]["id"],
            "team_name": team["team"]["name"],
            "league_id": league_id,
            "league_name": next(name for lid, name in leagues if lid == league_id)
        })
    return teams

def populate_teams_table(season):
    """Populate teams in MongoDB"""
    # Check if teams already exist
    existing_teams = store.get_team_ids(season)
    if existing_teams:
        print(f"Teams for season {season} already populated in MongoDB.")
        return

    all_teams = []
    for league_id, league_name in leagues:
        teams = get_teams_for_league(league_id, season)
        all_teams.extend(teams)
    
    store.store_teams(all_teams, season)

# ✅ NEW: MongoDB-specific query functions
def get_completed_fixtures_today():
    """Get today's completed fixtures from MongoDB"""
    return store.get_completed_fixtures(date.today())

def search_fixtures_by_team(team_name):
    """Search fixtures by team name in MongoDB"""
    return store.search_fixtures_by_team(team_name)

def get_database_stats():
    """Get MongoDB database statistics"""
    return store.get_stats()