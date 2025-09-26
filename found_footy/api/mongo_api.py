# ✅ FIXED: found_footy/api/mongo_api.py - Secure credentials from environment
import requests
from datetime import date
import json
import os
from prefect import task, get_run_logger

BASE_URL = "https://api-football-v1.p.rapidapi.com/v3"

# ✅ FIX: Load from environment variable - no fallback that exposes credentials
def get_api_headers():
    """Get API headers with secure credential handling"""
    rapidapi_key = os.getenv("RAPIDAPI_KEY")
    
    if not rapidapi_key:
        raise ValueError(
            "RAPIDAPI_KEY environment variable not set. "
            "Please add your RapidAPI key to the .env file: RAPIDAPI_KEY=your_key_here"
        )
    
    return {
        "x-rapidapi-key": rapidapi_key,
        "x-rapidapi-host": "api-football-v1.p.rapidapi.com",
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
    headers = get_api_headers()  # ✅ FIX: Use secure headers function
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
    headers = get_api_headers()  # ✅ FIX: Use secure headers function
    resp = requests.get(url, headers=headers, params={"ids": ids_str})
    resp.raise_for_status()
    return resp.json().get("response", [])  # raw items

def filter_fixtures_by_teams(fixtures_list, team_ids):
    """
    Filter on raw schema: item['teams']['home']['id'], item['teams']['away']['id']
    Accepts mixed inputs (raw or legacy flattened) without mutating items.
    """
    filtered = []
    ts = set(map(int, team_ids or []))
    for item in fixtures_list or []:
        try:
            # raw schema
            hid = int(item["teams"]["home"]["id"])
            aid = int(item["teams"]["away"]["id"])
        except Exception:
            # legacy flattened fallback
            hid = int(item.get("home_id", -1))
            aid = int(item.get("away_id", -1))
        if hid in ts or aid in ts:
            filtered.append(item)
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

def test_events_api_debug():
    """Debug the events API call specifically"""
    print("🔍 DEBUGGING EVENTS API")
    print("=" * 40)
    
    import requests
    
    # 1. Test direct API call
    headers = {
        'X-RapidAPI-Key': os.getenv('RAPIDAPI_KEY', ''),
        'X-RapidAPI-Host': 'api-football-v1.p.rapidapi.com'
    }
    
    # Test events endpoint directly
    fixture_id = 1378993
    url = f"https://api-football-v1.p.rapidapi.com/v3/fixtures/events?fixture={fixture_id}"
    
    print(f"🌐 Direct API call: {url}")
    
    try:
        response = requests.get(url, headers=headers, timeout=10)
        print(f"   Status: {response.status_code}")
        
        if response.status_code == 200:
            data = response.json()
            print(f"   Response keys: {list(data.keys())}")
            
            if 'response' in data:
                events = data['response']
                print(f"   Events array length: {len(events)}")
                
                for i, event in enumerate(events[:5]):  # Show first 5
                    event_type = event.get('type', 'NO_TYPE')
                    player_name = event.get('player', {}).get('name', 'NO_NAME')
                    minute = event.get('time', {}).get('elapsed', 'NO_TIME')
                    team_name = event.get('team', {}).get('name', 'NO_TEAM')
                    
                    print(f"      Event {i+1}: {event_type} - {player_name} ({team_name}) - {minute}'")
            else:
                print(f"   No 'response' key in data: {data}")
        else:
            print(f"   Error: {response.text}")
            
    except Exception as e:
        print(f"   ❌ Direct API call failed: {e}")
    
    # 2. Test your fixtures_events function
    print(f"\n🔧 Testing fixtures_events function...")
    
    try:
        from found_footy.api.mongo_api import fixtures_events
        
        events_data = fixtures_events([fixture_id])
        print(f"   Function returned: {len(events_data)} items")
        
        if events_data:
            for item in events_data:
                print(f"   Item keys: {list(item.keys())}")
                if 'events' in item:
                    print(f"   Events in item: {len(item['events'])}")
                else:
                    print(f"   No 'events' key in item")
        else:
            print("   Empty response from function")
            
    except Exception as e:
        print(f"   ❌ Function call failed: {e}")
        import traceback
        traceback.print_exc()