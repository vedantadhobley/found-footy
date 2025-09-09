from found_footy.storage.mongo_store import FootyMongoStore
import requests
from datetime import date, datetime
import json
import os
from prefect import task, get_run_logger

# Initialize MongoDB store
store = FootyMongoStore()

# API configuration
BASE_URL = "https://api-football-v1.p.rapidapi.com/v3"
HEADERS = {
    "x-rapidapi-key": "3815c39f56msh68991ec604f7be3p1cb7c4jsnf78a6cb47415",
    "x-rapidapi-host": "api-football-v1.p.rapidapi.com"
}

# ✅ NEW: Dynamic team loading from Prefect Variables
async def get_teams_from_variables(team_type="all"):
    """Get team data from Prefect Variables"""
    try:
        from prefect import get_client
        
        async with get_client() as client:
            teams = {}
            
            if team_type in ["all", "uefa", "clubs"]:
                try:
                    uefa_var = await client.read_variable_by_name("uefa_25_2025")
                    uefa_teams = json.loads(uefa_var.value)
                    # Convert string keys to integers
                    uefa_teams = {int(k): v for k, v in uefa_teams.items()}
                    teams.update(uefa_teams)
                except Exception as e:
                    print(f"⚠️ Could not load UEFA teams: {e}")
            
            if team_type in ["all", "fifa", "national"]:
                try:
                    fifa_var = await client.read_variable_by_name("fifa_25_2025")
                    fifa_teams = json.loads(fifa_var.value)
                    # Convert string keys to integers
                    fifa_teams = {int(k): v for k, v in fifa_teams.items()}
                    teams.update(fifa_teams)
                except Exception as e:
                    print(f"⚠️ Could not load FIFA teams: {e}")
            
            return teams
            
    except Exception as e:
        print(f"❌ Error loading teams from variables: {e}")
        return {}

def get_teams_sync(team_type="all"):
    """Synchronous wrapper for getting teams"""
    import asyncio
    try:
        return asyncio.run(get_teams_from_variables(team_type))
    except RuntimeError:
        # Handle case where event loop is already running
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(get_teams_from_variables(team_type))

async def get_team_ids_from_variables(team_type="all"):
    """Get team IDs list from Prefect Variables"""
    try:
        from prefect import get_client
        
        async with get_client() as client:
            variable_name = {
                "all": "all_teams_2025_ids",
                "uefa": "uefa_25_2025_ids", 
                "clubs": "uefa_25_2025_ids",
                "fifa": "fifa_25_2025_ids",
                "national": "fifa_25_2025_ids"
            }.get(team_type, "all_teams_2025_ids")
            
            var = await client.read_variable_by_name(variable_name)
            team_ids = [int(x.strip()) for x in var.value.split(",") if x.strip()]
            return team_ids
            
    except Exception as e:
        print(f"❌ Error loading team IDs: {e}")
        return []

def get_team_ids_sync(team_type="all"):
    """Synchronous wrapper for getting team IDs"""
    import asyncio
    try:
        return asyncio.run(get_team_ids_from_variables(team_type))
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(get_team_ids_from_variables(team_type))

# ✅ UPDATED: Team metadata functions using Prefect Variables
async def populate_team_metadata_async(reset_first=True, include_national_teams=True):
    """Populate team metadata from Prefect Variables"""
    from found_footy.storage.mongo_store import FootyMongoStore
    store = FootyMongoStore()
    
    if reset_first:
        print("🔄 Resetting MongoDB first...")
        store.drop_all_collections()
    
    # Load UEFA teams
    print("⚽ Populating UEFA club team metadata from variables...")
    uefa_teams = await get_teams_from_variables("uefa")
    
    for team_id, team_info in uefa_teams.items():
        team_data = {
            "team_id": team_id,
            "team_name": team_info["name"],
            "country": team_info["country"],
            "uefa_ranking": team_info["rank"],
            "team_type": "club"
        }
        store.store_team_metadata(team_data)
    
    print(f"✅ Populated {len(uefa_teams)} UEFA club teams")
    
    # Load FIFA teams if requested
    if include_national_teams:
        print("🌍 Populating FIFA national team metadata from variables...")
        fifa_teams = await get_teams_from_variables("fifa")
        
        for team_id, team_info in fifa_teams.items():
            team_data = {
                "team_id": team_id,
                "team_name": team_info["name"],
                "country": team_info["country"],
                "fifa_ranking": team_info["rank"],
                "team_type": "national"
            }
            store.store_team_metadata(team_data)
        
        print(f"✅ Populated {len(fifa_teams)} FIFA national teams")
    
    total_teams = len(uefa_teams) + (len(fifa_teams) if include_national_teams else 0)
    print(f"🎯 Total teams tracked: {total_teams}")

def populate_team_metadata(reset_first=True, include_national_teams=True):
    """Synchronous wrapper for populate_team_metadata_async"""
    import asyncio
    try:
        return asyncio.run(populate_team_metadata_async(reset_first, include_national_teams))
    except RuntimeError:
        import nest_asyncio
        nest_asyncio.apply()
        return asyncio.run(populate_team_metadata_async(reset_first, include_national_teams))

def get_available_teams(team_type="all"):
    """Get available teams by type from Prefect Variables"""
    return get_teams_sync(team_type)

# ✅ KEEP: All existing API functions unchanged
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
            event.get("detail") not in ["Penalty Shootout"]):
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
    
    # ✅ FIXED: Handle all possible input types
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
        return [team_ids_param]  # ✅ Handle single integer
    
    else:
        print(f"⚠️ Unexpected team_ids type: {type(team_ids_param)}")
        return []

@task(name="fixtures-store-bulk-task", retries=3, retry_delay_seconds=10)
def fixtures_store_bulk_task(staging_fixtures, active_fixtures):
    """Bulk store fixtures in their respective collections"""
    logger = get_run_logger()
    
    # ✅ UPDATED: Reference to fixtures_completed
    staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
    
    logger.info(f"💾 Stored: {staging_count} staging, {active_count} active")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count
    }