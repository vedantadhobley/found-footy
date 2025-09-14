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
    Return API-Football events exactly as returned by the API for a fixture (no filtering).
    Each item: { time: {...}, team: {...}, player: {...}, assist: {...}, type: "...", detail: "...", comments: ... }
    """
    url = f"{BASE_URL}/fixtures/events"
    headers = get_api_headers()  # ✅ FIX: Use secure headers function
    resp = requests.get(url, headers=headers, params={"fixture": str(fixture_id)})
    resp.raise_for_status()
    return resp.json().get("response", [])  # raw events

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