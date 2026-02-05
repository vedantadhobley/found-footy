# API-Football client - no orchestration dependencies
import os
from datetime import date

import requests

from src.utils.footy_logging import log, get_fallback_logger

MODULE = "api_client"
logger = get_fallback_logger()

# api-football.com direct endpoint
BASE_URL = "https://v3.football.api-sports.io"

# api-football.com authentication
def get_api_headers():
    """Get API headers for api-football.com access"""
    api_key = os.getenv("API_FOOTBALL_KEY")
    
    if not api_key:
        raise ValueError(
            "API_FOOTBALL_KEY environment variable not set. "
            "Please add your api-football.com key to the .env file: API_FOOTBALL_KEY=your_key_here"
        )
    
    return {
        "x-apisports-key": api_key
    }

def _coerce_date_param(date_param):
    if date_param is None:
        return date.today().strftime("%Y-%m-%d")
    if isinstance(date_param, date):
        return date_param.strftime("%Y-%m-%d")
    if isinstance(date_param, str) and len(date_param) == 8:
        return f"{date_param[:4]}-{date_param[4:6]}-{date_param[6:8]}"
    return str(date_param)

def get_fixtures_for_date(date_param=None):
    """
    Fetch fixtures for a specific date.
    Alias for fixtures() - cleaner name for Temporal activities.
    """
    return fixtures(date_param)


def fixtures(date_param=None):
    """
    Return API-Football fixtures exactly as returned by the API.
    Schema (per item): { fixture: {...}, league: {...}, teams: {...}, goals: {...}, score: {...} }
    """
    date_str = _coerce_date_param(date_param)
    url = f"{BASE_URL}/fixtures"
    headers = get_api_headers()
    resp = requests.get(url, headers=headers, params={"date": date_str}, timeout=10)
    resp.raise_for_status()
    items = resp.json().get("response", [])
    return items  # raw items

def fixtures_events(fixture_id):
    """
    Return API-Football events for a single fixture.
    Args: fixture_id - single fixture ID (int)
    Returns: List of event objects
    """
    url = f"{BASE_URL}/fixtures/events"
    headers = get_api_headers()
    
    resp = requests.get(url, headers=headers, params={"fixture": str(fixture_id)}, timeout=10)
    resp.raise_for_status()
    events = resp.json().get("response", [])
    
    return events  # Return raw events array, not wrapped in fixture object

def fixtures_batch(fixture_ids_list):
    """
    Batch fixtures by ids (raw schema).
    API-Football allows maximum 20 IDs per request, so we chunk if needed.
    """
    if not fixture_ids_list:
        return []
    
    MAX_IDS_PER_REQUEST = 20
    all_fixtures = []
    
    # Chunk the IDs if more than 20
    for i in range(0, len(fixture_ids_list), MAX_IDS_PER_REQUEST):
        chunk = fixture_ids_list[i:i + MAX_IDS_PER_REQUEST]
        ids_str = "-".join(map(str, chunk))
        url = f"{BASE_URL}/fixtures"
        headers = get_api_headers()
        resp = requests.get(url, headers=headers, params={"ids": ids_str}, timeout=10)
        resp.raise_for_status()
        
        data = resp.json()
        # Check for API errors (returned as 200 with errors field)
        if data.get("errors"):
            # API errors are important but not fatal - log at warning level
            log.warning(logger, MODULE, "api_errors", "API-Football returned errors", errors=data['errors'])
        
        all_fixtures.extend(data.get("response", []))
    
    return all_fixtures

def filter_fixtures_by_teams(fixtures_list, team_ids):
    """Filter fixtures that include any of the specified team IDs"""
    if not fixtures_list or not team_ids:
        return []
    
    team_ids_set = set(map(int, team_ids))
    filtered = []
    for fixture in fixtures_list:
        home_id = fixture.get("teams", {}).get("home", {}).get("id")
        away_id = fixture.get("teams", {}).get("away", {}).get("id")
        if home_id in team_ids_set or away_id in team_ids_set:
            filtered.append(fixture)
    return filtered

def parse_team_ids_parameter(team_ids_param):
    """Parse team IDs from parameter - ensure it always returns a list"""
    if team_ids_param is None:
        return []
    
    if isinstance(team_ids_param, str):
        if team_ids_param.strip() == "":
            return []
        try:
            # Try to parse as comma-separated values
            return [int(x.strip()) for x in team_ids_param.split(',') if x.strip()]
        except ValueError:
            return []
    
    if isinstance(team_ids_param, (list, tuple)):
        return [int(x) for x in team_ids_param if str(x).strip()]
    
    if isinstance(team_ids_param, int):
        return [team_ids_param]
    
    return []


def get_current_season(league_id: int) -> int | None:
    """
    Get the current season year for a league from API-Football.
    
    The API returns seasons array with 'current: true' for active season.
    European leagues use the starting year (2025 = 2025-26 season).
    
    Args:
        league_id: API-Football league ID
        
    Returns:
        Season year (e.g., 2025) or None if not found
    """
    url = f"{BASE_URL}/leagues"
    headers = get_api_headers()
    
    try:
        resp = requests.get(url, headers=headers, params={"id": league_id}, timeout=10)
        resp.raise_for_status()
        data = resp.json()
        
        leagues = data.get("response", [])
        if not leagues:
            log.warning(logger, MODULE, "league_not_found", "No league found", league_id=league_id)
            return None
        
        # Find the current season
        seasons = leagues[0].get("seasons", [])
        for season in seasons:
            if season.get("current") is True:
                return season.get("year")
        
        # Fallback: return the most recent season
        if seasons:
            latest = max(seasons, key=lambda s: s.get("year", 0))
            log.warning(logger, MODULE, "season_fallback", "No current season marked, using latest", league_id=league_id, season=latest.get('year'))
            return latest.get("year")
        
        return None
    except Exception as e:
        log.error(logger, MODULE, "season_fetch_failed", "Failed to get current season", league_id=league_id, error=str(e))
        return None


def get_teams_for_league(league_id: int, season: int) -> list[int]:
    """
    Get all team IDs for a league in a specific season.
    
    Args:
        league_id: API-Football league ID
        season: Season year (e.g., 2025 for 2025-26)
        
    Returns:
        List of team IDs
    """
    url = f"{BASE_URL}/teams"
    headers = get_api_headers()
    
    try:
        resp = requests.get(url, headers=headers, params={"league": league_id, "season": season}, timeout=10)
        resp.raise_for_status()
        data = resp.json()
        
        teams = data.get("response", [])
        team_ids = [t.get("team", {}).get("id") for t in teams if t.get("team", {}).get("id")]
        
        log.info(logger, MODULE, "teams_fetched", "Fetched teams for league", league_id=league_id, season=season, count=len(team_ids))
        return team_ids
    except Exception as e:
        log.error(logger, MODULE, "teams_fetch_failed", "Failed to get teams for league", league_id=league_id, season=season, error=str(e))
        return []


def get_team_info(team_id: int) -> dict | None:
    """
    Fetch full team info from API-Football.
    
    Response includes:
    - team.id: int
    - team.name: str (e.g., "Newcastle")
    - team.code: str (e.g., "NEW")
    - team.country: str (e.g., "England")
    - team.national: bool (True for national teams, False for clubs)
    - team.founded: int
    - team.logo: str (URL)
    - venue.name: str (e.g., "St. James' Park")
    - venue.city: str (e.g., "Newcastle upon Tyne")
    
    Args:
        team_id: API-Football team ID
        
    Returns:
        Full response dict with 'team' and 'venue' keys, or None if not found
    """
    url = f"{BASE_URL}/teams"
    headers = get_api_headers()
    
    try:
        resp = requests.get(url, headers=headers, params={"id": team_id})
        resp.raise_for_status()
        data = resp.json()
        
        response = data.get("response", [])
        if response and len(response) > 0:
            # Return the full response (team + venue), not just team
            return response[0]
        
        return None
    except Exception as e:
        log.warning(logger, MODULE, "team_info_failed", "Failed to fetch team info", team_id=team_id, error=str(e))
        return None


def is_national_team(team_id: int) -> bool | None:
    """
    Check if a team is a national team via API lookup.
    
    Args:
        team_id: API-Football team ID
        
    Returns:
        True if national team, False if club, None if lookup failed
    """
    full_info = get_team_info(team_id)
    if full_info:
        team = full_info.get("team", {})
        return team.get("national", False)
    return None

def test_events_api_debug():
    """Debug the events API call specifically"""
    print("🔍 DEBUGGING EVENTS API")
    print("=" * 40)
    
    import requests
    
    # Use api-football.com headers
    headers = get_api_headers()
    
    # Test events endpoint directly
    fixture_id = 1378993
    url = f"{BASE_URL}/fixtures/events?fixture={fixture_id}"
    
    print(f"🌐 api-football.com call: {url}")
    print(f"🔑 Headers: x-apisports-key: {headers['x-apisports-key'][:10]}...{headers['x-apisports-key'][-4:]}")
    
    try:
        response = requests.get(url, headers=headers, timeout=10)
        print(f"   Status: {response.status_code}")
        
        if response.status_code == 200:
            data = response.json()
            print(f"   Response keys: {list(data.keys())}")
            
            if 'response' in data:
                events = data['response']
                print(f"   Events found: {len(events)}")
                
                goal_events = [e for e in events if e.get('type') == 'Goal']
                print(f"   Goal events: {len(goal_events)}")
                
                for event in goal_events:
                    player = event.get('player', {}).get('name', 'Unknown')
                    team = event.get('team', {}).get('name', 'Unknown')
                    minute = event.get('time', {}).get('elapsed', '?')
                    print(f"     🥅 {player} ({team}) - {minute}'")
            else:
                print("   No 'response' key in data")
        else:
            print(f"   ❌ API Error: {response.status_code}")
            print(f"   Response: {response.text[:200]}")
            
    except Exception as e:
        print(f"   ❌ api-football.com call failed: {e}")
    
    print("\n2️⃣ Testing fixtures_events function...")
    try:
        events_data = fixtures_events(fixture_id)
        print(f"   Function returned: {len(events_data)} items")
        
        if events_data:
            goal_events = [e for e in events_data if e.get('type') == 'Goal']
            print(f"   Goal events from function: {len(goal_events)}")
        else:
            print("   Empty response from function")
            
    except Exception as e:
        print(f"   ❌ Function call failed: {e}")
        import traceback
        traceback.print_exc()