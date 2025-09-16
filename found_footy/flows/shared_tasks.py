import os
from datetime import datetime, timedelta, timezone
from prefect import task
from typing import Optional, List

from found_footy.api.mongo_api import (
    fixtures, 
    fixtures_batch, 
    fixtures_events,
    filter_fixtures_by_teams,
    parse_team_ids_parameter
)
from found_footy.utils.team_data import get_team_ids
# ✅ CLEAN: Simple direct import
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.utils.logging import get_logger, log_error_with_trace  # ✅ ADD

logger = get_logger(__name__)  # ✅ CHANGE

@task(name="fixtures-process-parameters-task", retries=1, retry_delay_seconds=5)
def fixtures_process_parameters_task(team_ids=None, date_str=None):
    """Parse and validate input parameters using team data from Prefect Variables"""
    
    # Process date
    if date_str is None:
        query_date = datetime.now(timezone.utc).date()
        logger.info(f"📅 Using today's date: {query_date}")
    else:
        try:
            query_date = datetime.strptime(date_str[:8], "%Y%m%d").date()
            logger.info(f"📅 Using specified date: {query_date}")
        except (ValueError, TypeError):
            logger.warning(f"⚠️ Invalid date format: {date_str}, using today")
            query_date = datetime.now(timezone.utc).date()
    
    # ✅ Get team IDs from Prefect Variables
    available_team_ids = get_team_ids()
    valid_team_ids = available_team_ids
    
    if team_ids and str(team_ids).strip() != "null":
        try:
            parsed_team_ids = parse_team_ids_parameter(team_ids)
            if parsed_team_ids:
                valid_team_ids = [tid for tid in parsed_team_ids if tid in available_team_ids]
                logger.info(f"⚽ Using {len(valid_team_ids)} specified teams")
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
    fixture_ids = [fixture["fixture"]["id"] for fixture in team_fixtures]
    logger.info(f"🔍 Getting detailed data for {len(fixture_ids)} fixtures...")
    
    try:
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
    except Exception as e:
        logger.warning(f"⚠️ Could not get detailed fixture data: {e}")
        api_lookup = {}
    
    for fixture in team_fixtures:
        fixture_id = fixture["fixture"]["id"]
        
        # Get detailed API data
        api_data = api_lookup.get(fixture_id, fixture)  # Use fixture as fallback
        
        # Extract using raw schema - don't flatten!
        status = api_data.get("fixture", {}).get("status", {}).get("short", "NS")
        
        # Use the raw fixture data as-is, don't modify schema
        processed_fixture = api_data
        
        # STATUS-BASED ROUTING using centralized logic
        if status in completed_statuses:
            completed_fixtures.append(processed_fixture)
            logger.debug(f"🏁 COMPLETED: {fixture_id} (status: {status})")
            
        elif status in active_statuses:
            active_fixtures.append(processed_fixture)
            logger.debug(f"🔄 ACTIVE: {fixture_id} (status: {status})")
            
        elif status in staging_statuses:
            staging_fixtures.append(processed_fixture)
            logger.debug(f"📅 STAGING: {fixture_id} (status: {status})")
                
        else:
            logger.warning(f"❓ UNKNOWN STATUS: {fixture_id} (status: {status}) - adding to staging")
            staging_fixtures.append(processed_fixture)
    
    logger.info(f"📊 STATUS CATEGORIZATION: {len(staging_fixtures)} staging, {len(active_fixtures)} active, {len(completed_fixtures)} completed")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures,
        "completed_fixtures": completed_fixtures
    }

@task(name="fixtures-store-task", retries=3, retry_delay_seconds=10)
def fixtures_store_task(staging_fixtures, active_fixtures, completed_fixtures):
    """Store fixtures in appropriate collections"""
    
    # ✅ CLEAN: Direct instantiation
    store = FootyMongoStore()
    
    staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
    completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_completed") if completed_fixtures else 0
    
    logger.info(f"💾 STORAGE: {staging_count} staging, {active_count} active, {completed_count} completed")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count,
        "completed_count": completed_count
    }

# found_footy/flows/shared_tasks.py - FIXED completion logic
def _should_complete_fixture(stored_fixture, api_fixture):
    """Enhanced completion logic - status AND goal-event sync required"""
    
    # Get completed statuses (same logic as fixtures_categorize_task)
    try:
        from found_footy.utils.fixture_status import get_fixture_statuses
        status_config = get_fixture_statuses()
        completed_statuses = set(status_config["completed"])
    except Exception:
        completed_statuses = {"FT", "AET", "PEN", "PST", "CANC", "ABD", "AWD", "WO"}
    
    # Check FIFA status
    api_status = api_fixture.get("fixture", {}).get("status", {}).get("short", "NS")
    if api_status not in completed_statuses:
        return False, "Status not completed"
    
    # Check goal-event synchronization
    api_goals = api_fixture.get("goals", {})
    total_goals = (api_goals.get("home", 0) or 0) + (api_goals.get("away", 0) or 0)
    
    # Get actual events count for this fixture
    fixture_id = api_fixture.get("fixture", {}).get("id")
    try:
        from found_footy.api.mongo_api import fixtures_events
        all_events = fixtures_events(fixture_id)
        goal_events = [e for e in all_events if e.get("type") == "Goal"]
        events_count = len(goal_events)
        
        if events_count != total_goals:
            return False, f"Goal-Event mismatch: {total_goals} goals but {events_count} events"
        
        return True, f"Status={api_status}, Goals={total_goals}, Events={events_count} ✅"
        
    except Exception as e:
        return False, f"Could not verify events: {e}"

@task(name="fixtures-delta-task")
def fixtures_delta_task():
    """Enhanced delta detection with goal-event synchronization"""
    
    # ✅ CLEAN: Direct instantiation
    store = FootyMongoStore()
    
    active_fixtures = store.get_active_fixtures()
    if not active_fixtures:
        logger.info("⏸️ No active fixtures for delta detection")
        return {
            "status": "success",
            "fixtures_with_changes": [], 
            "fixtures_completed": [], 
            "total_goals_detected": 0
        }
    
    fixture_ids = [store._extract_fixture_id(f) for f in active_fixtures]
    logger.info(f"🔍 Running bulk delta detection on {len(fixture_ids)} fixtures")
    
    try:
        # Bulk API call for all active fixtures
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
        
        fixtures_with_changes = []
        fixtures_completed = []
        total_goals_detected = 0

        for stored_fixture in active_fixtures:
            fixture_id = store._extract_fixture_id(stored_fixture)
            api_data = api_lookup.get(fixture_id)
            
            if not api_data:
                continue
                
            delta_result = store.fixtures_delta(fixture_id, api_data)
            
            # ✅ ENHANCED: Check completion with goal-event sync
            should_complete, reason = _should_complete_fixture(stored_fixture, api_data)
            
            if should_complete:
                logger.info(f"🏁 COMPLETION READY: Fixture {fixture_id} - {reason}")
                fixtures_completed.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result,
                    "completion_reason": reason
                })
            elif "FT" in api_data.get("fixture", {}).get("status", {}).get("short", ""):
                logger.warning(f"⏸️ COMPLETION BLOCKED: Fixture {fixture_id} - {reason}")
                # Keep in active for continued monitoring
            
            # Process goal changes as before
            if delta_result.get("goals_changed", False):
                fixtures_with_changes.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
        
        logger.info(f"🎯 DELTA RESULTS: {len(fixtures_with_changes)} goal changes, {len(fixtures_completed)} completions")
        
        return {
            "status": "success",
            "fixtures_with_changes": fixtures_with_changes,
            "fixtures_completed": fixtures_completed,
            "total_goals_detected": total_goals_detected
        }
        
    except Exception as e:
        logger.error(f"❌ Error in delta detection: {e}")
        return {
            "status": "error",
            "error": str(e),
            "fixtures_with_changes": [],
            "fixtures_completed": [],
            "total_goals_detected": 0
        }

@task(name="fixtures-advance-task")
def fixtures_advance_task(source_collection: str, destination_collection: str, fixture_id: Optional[int] = None):
    """Generic task to advance fixtures between collections"""
    
    # ✅ CLEAN: Direct instantiation
    store = FootyMongoStore()
    
    try:
        result = store.fixtures_advance(source_collection, destination_collection, fixture_id)
        
        if result["status"] == "success":
            logger.info(f"✅ Advanced {result['advanced_count']} fixtures: {source_collection} → {destination_collection}")
        else:
            logger.warning(f"⚠️ Advance failed: {result}")
        
        return result
        
    except Exception as e:
        logger.error(f"❌ Error in fixtures advance: {e}")
        return {"status": "error", "error": str(e), "advanced_count": 0}

# Add other task functions with the same clean pattern...