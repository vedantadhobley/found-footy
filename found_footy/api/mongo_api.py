# ✅ FIXED: found_footy/api/mongo_api.py - Remove all team metadata functions
import requests
from datetime import date, datetime
import json
import os
from prefect import task, get_run_logger

# API configuration
BASE_URL = "https://api-football-v1.p.rapidapi.com/v3"
HEADERS = {
    "x-rapidapi-key": "3815c39f56msh68991ec604f7be3p1cb7c4jsnf78a6cb47415",
    "x-rapidapi-host": "api-football-v1.p.rapidapi.com"
}

def fixtures(date_param=None):
    """Get fixtures - exact endpoint name from API"""
    if date_param is None:
        date_param = date.today().strftime("%Y-%m-%d")
    elif isinstance(date_param, date):
        date_param = date_param.strftime("%Y-%m-%d")
    elif len(date_param) == 8:  # YYYYMMDD format
        date_param = f"{date_param[:4]}-{date_param[4:6]}-{date_param[6:8]}"
    
    url = f"{BASE_URL}/fixtures"
    querystring = {"date": date_param}
    
    print(f"📡 API call: fixtures(date={date_param})")
    
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    
    api_fixtures = response.json().get("response", [])
    print(f"✅ API returned {len(api_fixtures)} fixtures")
    
    # Convert to simplified format
    simplified_fixtures = []
    for fixture in api_fixtures:
        simplified_fixtures.append({
            "id": fixture["fixture"]["id"],
            "home": fixture["teams"]["home"]["name"],
            "home_id": fixture["teams"]["home"]["id"],
            "away": fixture["teams"]["away"]["name"],
            "away_id": fixture["teams"]["away"]["id"],
            "league": fixture["league"]["name"],
            "league_id": fixture["league"]["id"],
            "time": fixture["fixture"]["date"]
        })
    
    return simplified_fixtures

def fixtures_events(fixture_id):
    """Get fixture events - exact endpoint name from API"""
    url = f"{BASE_URL}/fixtures/events"
    querystring = {"fixture": str(fixture_id)}
    
    print(f"📡 API call: fixtures/events(fixture={fixture_id})")
    
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    
    events = response.json().get("response", [])
    
    # Filter only goal events (non-penalty shootout)
    goal_events = []
    for event in events:
        if (event.get("type") == "Goal" and 
            event.get("detail") not in ["Missed Penalty"]):
            goal_events.append(event)
    
    print(f"✅ API returned {len(goal_events)} goal events for fixture {fixture_id}")
    return goal_events

def fixtures_batch(fixture_ids_list):
    """Get multiple fixtures in batch - using ids parameter"""
    if not fixture_ids_list:
        return []
    
    # Use hyphen-separated format for batch calls
    fixture_ids_str = "-".join(map(str, fixture_ids_list))
    
    url = f"{BASE_URL}/fixtures"
    querystring = {"ids": fixture_ids_str}
    
    print(f"📡 API call: fixtures(ids={fixture_ids_str}) - batch of {len(fixture_ids_list)}")
    
    response = requests.get(url, headers=HEADERS, params=querystring)
    response.raise_for_status()
    
    results = response.json().get("response", [])
    print(f"✅ Batch API returned {len(results)} fixture details")
    return results

def filter_fixtures_by_teams(fixtures_list, team_ids):
    """Filter fixtures to only include specified teams"""
    filtered = []
    for fixture in fixtures_list:
        if fixture["home_id"] in team_ids or fixture["away_id"] in team_ids:
            filtered.append(fixture)
    
    print(f"✅ Filtered to {len(filtered)} fixtures involving specified teams")
    return filtered

def parse_team_ids_parameter(team_ids_param):
    """Parse team IDs from parameter - ensure it always returns a list"""
    if team_ids_param is None or team_ids_param == "":
        return []
    
    if isinstance(team_ids_param, str):
        if team_ids_param.strip() == "":
            return []
        try:
            # Try JSON parsing first
            team_ids = json.loads(team_ids_param)
            if isinstance(team_ids, int):
                return [team_ids]  # Single integer in JSON
            elif isinstance(team_ids, list):
                return [int(x) for x in team_ids]  # List in JSON
            else:
                return []
        except json.JSONDecodeError:
            # Fall back to comma-separated parsing
            try:
                team_ids = [int(x.strip()) for x in team_ids_param.split(",") if x.strip()]
                return team_ids
            except ValueError:
                print(f"⚠️ Could not parse team_ids: {team_ids_param}")
                return []
    
    elif isinstance(team_ids_param, (list, tuple)):
        return [int(x) for x in team_ids_param]
    
    elif isinstance(team_ids_param, int):
        return [team_ids_param]
    
    else:
        print(f"⚠️ Unexpected team_ids type: {type(team_ids_param)}")
        return []