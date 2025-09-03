from datetime import datetime, timedelta, timezone
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from typing import Optional, List
from found_footy.api.mongo_api import (
    fixtures, 
    fixtures_batch, 
    fixtures_events,
    filter_fixtures_by_teams,
    populate_team_metadata,
    parse_team_ids_parameter,
    get_available_teams
)
from found_footy.utils.events import goal_trigger
from found_footy.storage.mongo_store import FootyMongoStore
from prefect.runtime import flow_run

# Create store instance
store = FootyMongoStore()

# ✅ SPLIT INGEST INTO MULTIPLE TASKS

@task(name="fixtures-process-parameters-task", retries=1, retry_delay_seconds=5)
def fixtures_process_parameters_task(team_ids=None, date_str=None):
    """Parse and validate input parameters"""
    logger = get_run_logger()
    
    # Process date
    if date_str is None:
        query_date = datetime.now(timezone.utc).date()
        logger.info(f"📅 Using today's date: {query_date}")
    else:
        try:
            query_date = datetime.strptime(date_str[:8], "%Y%m%d").date() if len(date_str) == 8 else datetime.strptime(date_str, "%Y-%m-%d").date()
        except (ValueError, TypeError):
            logger.warning(f"⚠️ Invalid date_str '{date_str}', using today")
            query_date = datetime.now(timezone.utc).date()
    
    # Process team IDs
    available_team_ids = store.get_team_ids()
    valid_team_ids = available_team_ids
    
    if team_ids and str(team_ids).strip() != "null":
        try:
            if isinstance(team_ids, str):
                if team_ids.startswith('['):
                    import json
                    target_team_ids = json.loads(team_ids)
                else:
                    target_team_ids = [int(x.strip()) for x in team_ids.split(",") if x.strip()]
            else:
                target_team_ids = [int(x) for x in team_ids] if isinstance(team_ids, list) else [team_ids]
            
            valid_team_ids = [tid for tid in target_team_ids if tid in available_team_ids]
        except Exception as e:
            logger.error(f"❌ Error parsing team_ids: {e}")
    
    logger.info(f"⚽ Using {len(valid_team_ids)} teams")
    
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
    
    # ✅ NEW: Get statuses from Prefect Variables
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
    
    for fixture in team_fixtures:
        # Get status from API call
        try:
            from found_footy.api.mongo_api import fixtures_batch
            api_data_list = fixtures_batch([fixture["id"]])
            
            if api_data_list:
                api_data = api_data_list[0]
                status = api_data.get("fixture", {}).get("status", {}).get("short", "NS")
            else:
                status = "NS"  # Default to not started if API fails
                
        except Exception as e:
            logger.warning(f"⚠️ Could not get status for fixture {fixture['id']}: {e}")
            status = "NS"  # Default to not started
        
        fixture["api_status"] = status
        
        # ✅ STATUS-BASED ROUTING using centralized logic
        if status in completed_statuses:
            completed_fixtures.append(fixture)
            logger.info(f"🏁 COMPLETED: {fixture['home']} vs {fixture['away']} (status: {status})")
            
        elif status in active_statuses:
            active_fixtures.append(fixture)
            logger.info(f"🔄 ACTIVE: {fixture['home']} vs {fixture['away']} (status: {status})")
            
        elif status in staging_statuses:
            # ✅ ADDITIONAL CHECK: Only staging if kickoff is in future
            kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
            if kickoff_time > current_time:
                staging_fixtures.append(fixture)
                logger.info(f"📅 STAGING: {fixture['home']} vs {fixture['away']} (status: {status}, kickoff: {kickoff_time.strftime('%H:%M')})")
            else:
                # Status says NS but time passed - treat as active for monitoring
                active_fixtures.append(fixture)
                logger.info(f"🔄 ACTIVE (late start): {fixture['home']} vs {fixture['away']} (status: {status})")
                
        else:
            logger.warning(f"⚠️ UNKNOWN STATUS: {fixture['home']} vs {fixture['away']} (status: {status}) - treating as active")
            active_fixtures.append(fixture)
    
    logger.info(f"📊 STATUS CATEGORIZATION: {len(staging_fixtures)} staging, {len(active_fixtures)} active, {len(completed_fixtures)} completed")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures,
        "completed_fixtures": completed_fixtures
    }

@task(name="fixtures-schedule-advances-task", retries=3, retry_delay_seconds=10)
def fixtures_schedule_advances_task(staging_fixtures):
    """Schedule fixtures-advance-flow for each staging fixture with rich names"""
    logger = get_run_logger()
    
    scheduled_advances = 0
    
    for fixture in staging_fixtures:
        kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
        advance_time = kickoff_time - timedelta(minutes=3)
        
        # ✅ ENHANCED: Rich contextual names for fixture advancement
        home_team = fixture["home"]
        away_team = fixture["away"]
        match_name = f"{home_team} vs {away_team}"
        kickoff_str = kickoff_time.strftime('%H:%M')
        
        flow_run_name = f"🚀 KICKOFF: {match_name} ({kickoff_str}) [#{fixture['id']}]"
        
        logger.info(f"📅 SCHEDULING advance for: {match_name}")
        
        try:
            run_deployment(
                name="fixtures-advance-flow/fixtures-advance-flow",
                parameters={
                    "source_collection": "fixtures_staging",
                    "destination_collection": "fixtures_active",
                    "fixture_id": fixture["id"]
                },
                scheduled_time=advance_time,
                flow_run_name=flow_run_name  # ✅ RICH NAME
            )
            scheduled_advances += 1
            logger.info(f"✅ SCHEDULED: {match_name}")
            
        except Exception as e:
            logger.error(f"❌ Failed to schedule {match_name}: {e}")
    
    return scheduled_advances

@task(name="fixtures-store-bulk-task", retries=3, retry_delay_seconds=10)
def fixtures_store_bulk_task(staging_fixtures, active_fixtures):
    """Bulk store fixtures in their respective collections"""
    logger = get_run_logger()
    
    staging_count = store.bulk_insert_fixtures_staging(staging_fixtures) if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures_active(active_fixtures) if active_fixtures else 0
    
    logger.info(f"💾 Stored: {staging_count} staging, {active_count} active")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count
    }

@task(name="fixtures-delta-task")
def fixtures_delta_task():
    """Bulk delta detection for entire fixtures_active collection"""
    logger = get_run_logger()
    
    active_fixtures = store.get_all_active_fixtures()
    if not active_fixtures:
        logger.info("⏸️ No active fixtures for delta detection")
        return {"fixtures_with_changes": [], "fixtures_completed": [], "total_goals_detected": 0}
    
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
                continue
            
            # ✅ EXISTING METHOD: Pure delta detection
            delta_result = store.fixtures_delta(fixture_id, api_data)
            
            if delta_result.get("goals_changed", False):
                fixtures_with_changes.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
                total_goals_detected += delta_result.get("total_goal_increase", 0)
                logger.info(f"🚨 Goals changed for fixture {fixture_id}: +{delta_result.get('total_goal_increase', 0)}")
            
            if delta_result.get("fixture_completed", False):
                fixtures_completed.append({
                    "fixture_id": fixture_id,
                    "delta_result": delta_result
                })
                logger.info(f"🏁 Fixture {fixture_id} completed")
        
        logger.info(f"✅ Delta detection complete: {len(fixtures_with_changes)} with goals, {len(fixtures_completed)} completed")
        
        return {
            "fixtures_with_changes": fixtures_with_changes,
            "fixtures_completed": fixtures_completed,
            "total_goals_detected": total_goals_detected,
            "status": "success"
        }
        
    except Exception as e:
        logger.error(f"❌ Bulk delta detection failed: {e}")
        return {"fixtures_with_changes": [], "fixtures_completed": [], "total_goals_detected": 0, "status": "error", "error": str(e)}

@task(name="fixtures-monitor-task")
def fixtures_monitor_task():
    """Monitor active fixtures using bulk delta detection"""
    logger = get_run_logger()
    
    # ✅ BULK DELTA: Run delta detection on entire collection
    delta_results = fixtures_delta_task()
    
    if delta_results["status"] != "success":
        logger.error("❌ Delta detection failed")
        return {"status": "error", "delta_results": delta_results}
    
    total_goals_processed = 0
    completed_fixtures_processed = 0
    
    # ✅ PROCESS GOALS: Handle all goal changes using existing store method
    for fixture_change in delta_results["fixtures_with_changes"]:
        fixture_id = fixture_change["fixture_id"]
        delta_result = fixture_change["delta_result"]
        
        try:
            # ✅ EXISTING METHOD: Handle goal changes with side effects
            change_result = store.handle_fixture_changes(fixture_id, delta_result)
            goals_processed = change_result.get("goals_processed", 0)
            total_goals_processed += goals_processed
            logger.info(f"⚽ Processed {goals_processed} new goals for fixture {fixture_id}")
        except Exception as e:
            logger.error(f"❌ Error processing goals for fixture {fixture_id}: {e}")
    
    # ✅ PROCESS COMPLETIONS: Handle completed fixtures with CLEAN names
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"]
        delta_result = completed_fixture["delta_result"]
        
        try:
            # ✅ GET: Fixture details for rich completion name
            fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
            if fixture:
                home_team = fixture.get("team_names", {}).get("home", "Home")
                away_team = fixture.get("team_names", {}).get("away", "Away")
                home_score = delta_result.get("current_goals", {}).get("home", 0)
                away_score = delta_result.get("current_goals", {}).get("away", 0)
                status = delta_result.get("api_status", "FT")
                
                # ✅ CLEAN: Team names + score + status only (no "Final Processing")
                flow_run_name = f"🏁 {home_team} {home_score}-{away_score} {away_team} ({status})"
            else:
                flow_run_name = f"🏁 Match #{fixture_id}"
            
            run_deployment(
                name="fixtures-advance-flow/fixtures-advance-flow",
                parameters={
                    "source_collection": "fixtures_active",
                    "destination_collection": "fixtures_processed",
                    "fixture_id": fixture_id
                },
                flow_run_name=flow_run_name  # ✅ CLEAN COMPLETION NAME
            )
            completed_fixtures_processed += 1
            logger.info(f"✅ Scheduled completion flow for fixture {fixture_id}")
        except Exception as e:
            logger.error(f"❌ Failed to process completion for fixture {fixture_id}: {e}")
    
    return {
        "status": "success",
        "active_fixtures": len(store.get_all_active_fixtures()),
        "goals_detected": delta_results["total_goals_detected"],
        "goals_processed": total_goals_processed,
        "completed_fixtures": completed_fixtures_processed,
        "delta_results": delta_results
    }

# ✅ FLOWS WITH CONSISTENT NAMING

@flow(name="fixtures-ingest-flow")
def fixtures_ingest_flow(date_str: Optional[str] = None, team_ids: Optional[str] = None):
    """Pure ingestion with status-based routing"""
    logger = get_run_logger()
    
    logger.info("📥 Starting Pure Fixtures Ingest Flow")
    
    # Initialize team metadata
    populate_team_metadata(reset_first=False)
    
    # Tasks with simplified names
    params = fixtures_process_parameters_task(team_ids, date_str)
    team_fixtures = fixtures_fetch_api_task(params["query_date"], params["valid_team_ids"])
    
    if not team_fixtures:
        return {"status": "no_fixtures", "message": "No fixtures found"}
    
    # ✅ SIMPLIFIED NAME: fixtures-categorize-task
    categorized = fixtures_categorize_task(team_fixtures)
    
    # Schedule advances for staging fixtures
    scheduled_advances = fixtures_schedule_advances_task(categorized["staging_fixtures"])
    
    # ✅ SIMPLIFIED NAME: fixtures-store-task
    storage_result = fixtures_store_task(
        categorized["staging_fixtures"], 
        categorized["active_fixtures"],
        categorized["completed_fixtures"]
    )
    
    logger.info("✅ Pure status-based ingestion complete - live monitoring only")
    
    return {
        "status": "success",
        "approach": "pure_status_based_live_only",
        "staging_fixtures": storage_result["staging_count"],
        "active_fixtures": storage_result["active_count"],
        "completed_fixtures": storage_result["completed_count"],
        "scheduled_advances": scheduled_advances,
        "note": "Historical fixtures moved to completed - live monitoring only"
    }

@flow(name="fixtures-advance-flow")
def fixtures_advance_flow(
    source_collection: str = "fixtures_staging", 
    destination_collection: str = "fixtures_active",
    fixture_id: Optional[int] = None
):
    """PURE fixture advancement - no goal processing, monitor handles everything"""
    logger = get_run_logger()
    
    logger.info(f"📋 Pure advancement: {source_collection} → {destination_collection}")
    
    if fixture_id:
        logger.info(f"🎯 Processing specific fixture: {fixture_id}")
    
    # Use the universal task
    advance_result = fixtures_advance_task(source_collection, destination_collection, fixture_id)
    
    # ✅ SIMPLIFIED: Pure advancement with status logging only
    if advance_result["status"] == "success" and advance_result["advanced_count"] > 0:
        
        if destination_collection == "fixtures_active":
            logger.info(f"✅ Fixture {fixture_id} promoted to active - monitor will detect live goals")
        elif destination_collection == "fixtures_processed":
            logger.info(f"🏁 Fixture {fixture_id} archived to processed - no further monitoring")
        else:
            logger.info(f"✅ Generic advancement: {advance_result['advanced_count']} fixtures")
    
    return {
        "status": advance_result["status"],
        "source_collection": source_collection,
        "destination_collection": destination_collection,
        "fixture_id": fixture_id,
        "advanced_count": advance_result.get("advanced_count", 0),
        "note": "Pure advancement - monitor handles all live goal detection"
    }

@flow(name="fixtures-monitor-flow")
def fixtures_monitor_flow():
    """Always-running monitor - runs every 3 minutes"""
    logger = get_run_logger()
    
    # ✅ ADD: Set rich name with timestamp at runtime
    try:
        current_time = datetime.now().strftime("%H:%M:%S")
        flow_run.name = f"👁️ MONITOR: {current_time} - Active Check"
    except Exception as e:
        logger.warning(f"Could not set flow name: {e}")
    
    # ✅ NEVER EXIT: Just skip work efficiently
    if store.check_collections_empty(["fixtures_active"]):
        logger.info("⏸️ No active fixtures - skipping API calls (monitor continues)")
        return {
            "status": "no_work_skipped", 
            "reason": "no_active_fixtures",
            "next_check": "3 minutes (automatic)",
            "monitor_status": "running_continuously"
        }
    
    # ✅ DO WORK: Monitor active fixtures
    logger.info("🔍 Active fixtures found - performing monitoring")
    monitor_result = fixtures_monitor_task()
    
    logger.info(f"✅ Monitor cycle complete: {monitor_result}")
    
    return {
        "status": "work_completed", 
        "monitor_result": monitor_result,
        "next_check": "3 minutes (automatic)",
        "monitor_status": "running_continuously"
    }

# ✅ ADD THIS MISSING METHOD:

@task(name="fixtures-advance-task")
def fixtures_advance_task(source_collection: str, destination_collection: str, fixture_id: Optional[int] = None):
    """Generic task to advance fixtures between collections"""
    logger = get_run_logger()
    
    try:
        if fixture_id:
            logger.info(f"📋 Advancing single fixture {fixture_id}: {source_collection} → {destination_collection}")
        else:
            logger.info(f"📋 Advancing all eligible fixtures: {source_collection} → {destination_collection}")
        
        # Use the store's generic method
        result = store.fixtures_advance(source_collection, destination_collection, fixture_id)
        
        logger.info(f"✅ Advanced {result.get('advanced_count', 0)} fixtures")
        return result
        
    except Exception as e:
        logger.error(f"❌ Error in fixtures-advance-task: {e}")
        return {"status": "error", "advanced_count": 0, "error": str(e)}

@task(name="fixtures-categorize-task")
def fixtures_categorize_task(team_fixtures):
    """Categorize fixtures by FIFA status - pure status-driven routing"""
    logger = get_run_logger()
    
    staging_fixtures = []
    active_fixtures = []
    completed_fixtures = []
    
    # ✅ NEW: Get statuses from Prefect Variables
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
    
    for fixture in team_fixtures:
        # Get status from API call
        try:
            from found_footy.api.mongo_api import fixtures_batch
            api_data_list = fixtures_batch([fixture["id"]])
            
            if api_data_list:
                api_data = api_data_list[0]
                status = api_data.get("fixture", {}).get("status", {}).get("short", "NS")
            else:
                status = "NS"  # Default to not started if API fails
                
        except Exception as e:
            logger.warning(f"⚠️ Could not get status for fixture {fixture['id']}: {e}")
            status = "NS"  # Default to not started
        
        fixture["api_status"] = status
        
        # ✅ STATUS-BASED ROUTING using centralized logic
        if status in completed_statuses:
            completed_fixtures.append(fixture)
            logger.info(f"🏁 COMPLETED: {fixture['home']} vs {fixture['away']} (status: {status})")
            
        elif status in active_statuses:
            active_fixtures.append(fixture)
            logger.info(f"🔄 ACTIVE: {fixture['home']} vs {fixture['away']} (status: {status})")
            
        elif status in staging_statuses:
            # ✅ ADDITIONAL CHECK: Only staging if kickoff is in future
            kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
            if kickoff_time > current_time:
                staging_fixtures.append(fixture)
                logger.info(f"📅 STAGING: {fixture['home']} vs {fixture['away']} (status: {status}, kickoff: {kickoff_time.strftime('%H:%M')})")
            else:
                # Status says NS but time passed - treat as active for monitoring
                active_fixtures.append(fixture)
                logger.info(f"🔄 ACTIVE (late start): {fixture['home']} vs {fixture['away']} (status: {status})")
                
        else:
            logger.warning(f"⚠️ UNKNOWN STATUS: {fixture['home']} vs {fixture['away']} (status: {status}) - treating as active")
            active_fixtures.append(fixture)
    
    logger.info(f"📊 STATUS CATEGORIZATION: {len(staging_fixtures)} staging, {len(active_fixtures)} active, {len(completed_fixtures)} completed")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures,
        "completed_fixtures": completed_fixtures
    }

@task(name="fixtures-store-task", retries=3, retry_delay_seconds=10)
def fixtures_store_task(staging_fixtures, active_fixtures, completed_fixtures):
    """Store fixtures in appropriate collections"""
    logger = get_run_logger()
    
    staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
    completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_processed") if completed_fixtures else 0
    
    logger.info(f"💾 STORAGE: {staging_count} staging, {active_count} active, {completed_count} completed")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count,
        "completed_count": completed_count
    }