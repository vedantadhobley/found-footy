# ✅ REVERT: Back to RapidAPI for batch support
import json
import os
from datetime import date

import requests
from prefect import get_run_logger, task

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

def fixtures(date_param=None):
    """
    Return API-Football fixtures exactly as returned by the API.
    Schema (per item): { fixture: {...}, league: {...}, teams: {...}, goals: {...}, score: {...} }
    """
    date_str = _coerce_date_param(date_param)
    url = f"{BASE_URL}/fixtures"
    headers = get_api_headers()
    resp = requests.get(url, headers=headers, params={"date": date_str})
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
    
    resp = requests.get(url, headers=headers, params={"fixture": str(fixture_id)})
    resp.raise_for_status()
    events = resp.json().get("response", [])
    
    return events  # Return raw events array, not wrapped in fixture object

def fixtures_batch(fixture_ids_list):
    """
    Batch fixtures by ids (raw schema).
    """
    if not fixture_ids_list:
        return []
    ids_str = "-".join(map(str, fixture_ids_list))
    url = f"{BASE_URL}/fixtures"
    headers = get_api_headers()
    resp = requests.get(url, headers=headers, params={"ids": ids_str})
    resp.raise_for_status()
    return resp.json().get("response", [])  # raw items

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

def test_events_api_debug():
    """Debug the events API call specifically"""
    print("🔍 DEBUGGING EVENTS API")
    print("=" * 40)
    
    import requests
    
    # ✅ FIX: Use RapidAPI headers consistently
    headers = get_api_headers()
    
    # Test events endpoint directly
    fixture_id = 1378993
    url = f"{BASE_URL}/fixtures/events?fixture={fixture_id}"
    
    print(f"🌐 RapidAPI call: {url}")
    print(f"🔑 Headers: X-RapidAPI-Key: {headers['X-RapidAPI-Key'][:10]}...{headers['X-RapidAPI-Key'][-4:]}")  # ✅ FIX: Correct header key
    
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
        print(f"   ❌ RapidAPI call failed: {e}")
    
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