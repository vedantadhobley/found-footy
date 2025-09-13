import os  # ✅ ADD THIS LINE
from datetime import datetime, timedelta, timezone
from prefect import task, get_run_logger
from typing import Optional, List

from found_footy.api.mongo_api import (
    fixtures, 
    fixtures_batch, 
    fixtures_events,
    filter_fixtures_by_teams,
    parse_team_ids_parameter
)
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.utils.team_data import get_team_ids

# Create store instance
store = FootyMongoStore()

@task(name="fixtures-process-parameters-task", retries=1, retry_delay_seconds=5)
def fixtures_process_parameters_task(team_ids=None, date_str=None):
    """Parse and validate input parameters using team data from Prefect Variables"""
    logger = get_run_logger()
    
    # Process date
    if date_str is None:
        query_date = datetime.now(timezone.utc).date()
        logger.info(f"📅 Using today's date: {query_date}")
    else:
        try:
            if len(date_str) == 8:
                query_date = datetime.strptime(date_str, "%Y%m%d").date()  # ✅ FIX: Complete the line
            else:
                query_date = datetime.strptime(date_str[:10], "%Y-%m-%d").date()  # ✅ FIX: Complete the line
            logger.info(f"📅 Using provided date: {query_date}")
        except (ValueError, TypeError):
            query_date = datetime.now(timezone.utc).date()
            logger.warning(f"⚠️ Invalid date format, using today: {query_date}")
    
    # ✅ Get team IDs from Prefect Variables
    available_team_ids = get_team_ids()
    valid_team_ids = available_team_ids
    
    if team_ids and str(team_ids).strip() != "null":
        try:
            parsed_team_ids = parse_team_ids_parameter(team_ids)
            if parsed_team_ids:
                valid_team_ids = [tid for tid in parsed_team_ids if tid in available_team_ids]  # ✅ FIX: Complete the line
                logger.info(f"⚽ Using {len(valid_team_ids)} parsed team IDs")
            else:
                logger.info(f"⚽ No valid parsed team IDs, using all teams")  # ✅ FIX: Complete the line
        except Exception as e:
            logger.warning(f"⚠️ Error parsing team_ids: {e}, using all teams")
    
    logger.info(f"⚽ Using {len(valid_team_ids)} teams from Prefect Variables")
    
    return {
        "query_date": query_date,
        "valid_team_ids": valid_team_ids
    }

@task(name="fixtures-fetch-api-task", retries=2, retry_delay_seconds=30)
def fixtures_fetch_api_task(query_date, valid_team_ids):
    """Fetch and filter fixtures from API"""
    logger = get_run_logger()
    
    # API calls
    all_fixtures = fixtures(query_date)
    team_fixtures = filter_fixtures_by_teams(all_fixtures, valid_team_ids)
    
    if not team_fixtures:
        logger.warning("❌ No fixtures found")
        return []
    
    logger.info(f"✅ Found {len(team_fixtures)} relevant fixtures")
    return team_fixtures

@task(name="fixtures-categorize-task")
def fixtures_categorize_task(team_fixtures):
    """Categorize fixtures by FIFA status - pure status-driven routing"""
    logger = get_run_logger()
    
    staging_fixtures = []
    active_fixtures = []
    completed_fixtures = []
    
    # Get statuses from Prefect Variables
    try:
        from found_footy.utils.fixture_status import get_fixture_statuses
        status_config = get_fixture_statuses()
        
        completed_statuses = set(status_config["completed"])
        active_statuses = set(status_config["active"])
        staging_statuses = set(status_config["staging"])
    except Exception as e:
        logger.warning(f"⚠️ Could not load status config: {e}")
        # Fallback to hardcoded values
        completed_statuses = {"FT", "AET", "PEN", "PST", "CANC", "ABD", "AWD", "WO"}
        active_statuses = {"1H", "HT", "2H", "ET", "BT", "P", "SUSP", "INT", "LIVE"}
        staging_statuses = {"TBD", "NS"}
    
    current_time = datetime.now(timezone.utc)
    
    # Get full fixture data including current scores
    fixture_ids = [fixture["id"] for fixture in team_fixtures]
    logger.info(f"🔍 Getting detailed data for {len(fixture_ids)} fixtures...")
    
    try:
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
    except Exception as e:
        logger.warning(f"⚠️ Could not get detailed fixture data: {e}")
        api_lookup = {}
    
    for fixture in team_fixtures:
        fixture_id = fixture["id"]
        
        # Get detailed API data
        api_data = api_lookup.get(fixture_id, {})
        status = api_data.get("fixture", {}).get("status", {}).get("short", "NS")
        
        # Extract current scores from API
        goals_data = api_data.get("goals", {"home": 0, "away": 0})
        current_home_goals = goals_data.get("home") or 0
        current_away_goals = goals_data.get("away") or 0
        
        # ✅ FIX: Ensure team names are preserved in the correct format
        fixture["api_status"] = status
        fixture["status"] = status
        fixture["current_goals"] = {
            "home": current_home_goals,
            "away": current_away_goals
        }
        fixture["goals"] = {
            "home": current_home_goals,
            "away": current_away_goals
        }
        # ✅ ENSURE: Team names are accessible as 'home' and 'away' fields
        # These should already be set from the mongo_api.fixtures() call
        # fixture["home"] and fixture["away"] should already exist
        
        # STATUS-BASED ROUTING using centralized logic
        if status in completed_statuses:
            completed_fixtures.append(fixture)
            logger.info(f"🏁 COMPLETED: {fixture['home']} vs {fixture['away']} (status: {status})")
            
        elif status in active_statuses:
            active_fixtures.append(fixture)
            logger.info(f"🔄 ACTIVE: {fixture['home']} {current_home_goals}-{current_away_goals} {fixture['away']} (status: {status})")
            
        elif status in staging_statuses:
            kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
            if kickoff_time > current_time:
                staging_fixtures.append(fixture)
                logger.info(f"📅 STAGING: {fixture['home']} vs {fixture['away']} (kickoff: {kickoff_time})")
            else:
                # Past fixture but still NS - likely cancelled or error
                logger.warning(f"⚠️ Past fixture still NS: {fixture['home']} vs {fixture['away']}")
                staging_fixtures.append(fixture)
                
        else:
            # Unknown status - default to staging
            staging_fixtures.append(fixture)
            logger.warning(f"❓ UNKNOWN STATUS: {fixture['home']} vs {fixture['away']} (status: {status}) - defaulted to staging")
    
    logger.info(f"📊 STATUS CATEGORIZATION: {len(staging_fixtures)} staging, {len(active_fixtures)} active, {len(completed_fixtures)} completed")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures,
        "completed_fixtures": completed_fixtures
    }

@task(name="fixtures-store-task", retries=3, retry_delay_seconds=10)
def fixtures_store_task(staging_fixtures, active_fixtures, completed_fixtures):
    """Store fixtures in appropriate collections - handles all 3 types"""
    logger = get_run_logger()
    
    staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
    completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_completed") if completed_fixtures else 0
    
    logger.info(f"💾 STORAGE: {staging_count} staging, {active_count} active, {completed_count} completed")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count,
        "completed_count": completed_count
    }

@task(name="fixtures-delta-task")
def fixtures_delta_task():
    """Bulk delta detection for entire fixtures_active collection"""
    logger = get_run_logger()
    
    # ✅ FIX: Use the correct method name
    active_fixtures = store.get_active_fixtures()
    if not active_fixtures:
        logger.info("⏸️ No active fixtures for delta detection")
        return {
            "status": "success",
            "fixtures_with_changes": [], 
            "fixtures_completed": [], 
            "total_goals_detected": 0
        }
    
    fixture_ids = [f["fixture_id"] for f in active_fixtures]
    logger.info(f"🔍 Running bulk delta detection on {len(fixture_ids)} fixtures")
    
    try:
        # Bulk API call for all active fixtures
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
        
        fixtures_with_changes = []
        fixtures_completed = []
        total_goals_detected = 0
        
        for active_fixture in active_fixtures:
            fixture_id = active_fixture["fixture_id"]
            api_data = api_lookup.get(fixture_id)
            
            if not api_data:
                logger.warning(f"⚠️ No API data for fixture {fixture_id}")  # ✅ FIX: Complete the line
                continue
                
            # Get delta for this fixture
            delta_result = store.fixtures_delta(fixture_id, api_data)  # ✅ FIX: Complete the line
            
            if delta_result.get("goals_changed"):
                fixtures_with_changes.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
                total_goals_detected += delta_result.get("total_goal_increase", 0)
            
            if delta_result.get("status_changed_to_completed"):
                fixtures_completed.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
        
        logger.info(f"✅ Delta detection complete: {len(fixtures_with_changes)} with goals, {len(fixtures_completed)} completed")
        
        return {
            "status": "success",
            "fixtures_with_changes": fixtures_with_changes,
            "fixtures_completed": fixtures_completed,
            "total_goals_detected": total_goals_detected
        }
        
    except Exception as e:
        logger.error(f"❌ Bulk delta detection failed: {e}")
        return {
            "status": "error", 
            "fixtures_with_changes": [], 
            "fixtures_completed": [], 
            "total_goals_detected": 0, 
            "error": str(e)
        }

@task(name="fixtures-advance-task")
def fixtures_advance_task(source_collection: str, destination_collection: str, fixture_id: Optional[int] = None):
    """Generic task to advance fixtures between collections"""
    logger = get_run_logger()
    
    try:
        if fixture_id:
            logger.info(f"📋 Advancing specific fixture {fixture_id}: {source_collection} → {destination_collection}")  # ✅ FIX: Complete the line
        else:
            logger.info(f"📋 Advancing all fixtures: {source_collection} → {destination_collection}")  # ✅ FIX: Complete the line
        
        # Use the store's generic method
        result = store.fixtures_advance(source_collection, destination_collection, fixture_id)
        logger.info(f"✅ Advanced {result.get('advanced_count', 0)} fixtures")
        
        return result
        
    except Exception as e:
        logger.error(f"❌ Error in fixtures advance task: {e}")
        return {"status": "error", "advanced_count": 0, "error": str(e)}