from datetime import datetime, timedelta, timezone
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from typing import Optional, List
import json
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
from found_footy.flows.flow_triggers import schedule_advance

# Create store instance
store = FootyMongoStore()

# ✅ SPLIT INGEST INTO MULTIPLE TASKS

# ✅ FIX: Complete the team_ids parsing logic
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
                # ✅ FIX: Complete the parsing logic
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
    
    # ✅ ENHANCED: Get full fixture data including current scores
    fixture_ids = [fixture["id"] for fixture in team_fixtures]
    logger.info(f"🔍 Getting detailed data for {len(fixture_ids)} fixtures...")
    
    try:
        from found_footy.api.mongo_api import fixtures_batch
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
        
        # ✅ CRITICAL: Extract current scores from API
        goals_data = api_data.get("goals", {"home": 0, "away": 0})
        current_home_goals = goals_data.get("home") or 0
        current_away_goals = goals_data.get("away") or 0
        
        # Add API data to fixture
        fixture["api_status"] = status
        fixture["status"] = status
        # ✅ ADD: Current scores for proper delta detection
        fixture["current_goals"] = {
            "home": current_home_goals,
            "away": current_away_goals
        }
        
        # ✅ STATUS-BASED ROUTING using centralized logic
        if status in completed_statuses:
            completed_fixtures.append(fixture)
            logger.info(f"🏁 COMPLETED: {fixture['home']} {current_home_goals}-{current_away_goals} {fixture['away']} (status: {status})")
            
        elif status in active_statuses:
            active_fixtures.append(fixture)
            logger.info(f"🔄 ACTIVE: {fixture['home']} {current_home_goals}-{current_away_goals} {fixture['away']} (status: {status})")
            
        elif status in staging_statuses:
            # ✅ ADDITIONAL CHECK: Only staging if kickoff is in future
            kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
            if kickoff_time > current_time:
                staging_fixtures.append(fixture)
                logger.info(f"📅 STAGING: {fixture['home']} vs {fixture['away']} (status: {status}, kickoff: {kickoff_time.strftime('%H:%M')})")
            else:
                # Status says NS but time passed - treat as active for monitoring
                active_fixtures.append(fixture)
                logger.info(f"🔄 ACTIVE (late start): {fixture['home']} {current_home_goals}-{current_away_goals} {fixture['away']} (status: {status})")
                
        else:
            logger.warning(f"⚠️ UNKNOWN STATUS: {fixture['home']} vs {fixture['away']} (status: {status}) - treating as active")
            active_fixtures.append(fixture)
    
    logger.info(f"📊 STATUS CATEGORIZATION: {len(staging_fixtures)} staging, {len(active_fixtures)} active, {len(completed_fixtures)} completed")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures,
        "completed_fixtures": completed_fixtures
    }

# ✅ REMOVE: fixtures_store_bulk_task (this is the old 2-parameter version)
# We don't need this anymore

# ✅ KEEP ONLY: fixtures_store_task (handles all 3 collections)
@task(name="fixtures-store-task", retries=3, retry_delay_seconds=10)
def fixtures_store_task(staging_fixtures, active_fixtures, completed_fixtures):
    """Store fixtures in appropriate collections - handles all 3 types"""
    logger = get_run_logger()
    
    # ✅ SIMPLE: Use existing method - it now handles current scores automatically
    staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
    completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_processed") if completed_fixtures else 0
    
    logger.info(f"💾 STORAGE: {staging_count} staging, {active_count} active, {completed_count} completed")
    
    return {
        "staging_count": staging_count,
        "active_count": active_count,
        "completed_count": completed_count
    }

# ✅ RENAME: fixtures_schedule_advances_task → fixtures_schedule_task
# @task(name="fixtures-schedule-task", retries=3, retry_delay_seconds=10)
# def fixtures_schedule_task(staging_fixtures):
#     """Schedule advance flows for staging fixtures - NON-BLOCKING"""
#     logger = get_run_logger()
#     scheduled_advances = 0

#     for i, fixture in enumerate(staging_fixtures):
#         logger.info(f"⏳ Processing fixture {i+1}/{len(staging_fixtures)}: {fixture['home']} vs {fixture['away']}")
#         try:
#             kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
#             advance_time = kickoff_time - timedelta(minutes=3)
            
#             # Use subprocess to avoid blocking
#             import subprocess
#             fixture_id = fixture["id"]
#             home_team = fixture["home"]
#             away_team = fixture["away"]
            
#             subprocess.Popen([
#                 "prefect", "deployment", "run", 
#                 "fixtures-advance-flow/fixtures-advance-flow",
#                 "--param", f"source_collection=fixtures_staging",
#                 "--param", f"destination_collection=fixtures_active", 
#                 "--param", f"fixture_id={fixture_id}",
#                 "--start-in", f"{int((advance_time - datetime.now(timezone.utc)).total_seconds())}s"
#             ])
            
#             scheduled_advances += 1
#             logger.info(f"✅ Scheduled advance for: {fixture['home']} vs {fixture['away']} at {advance_time}")
#         except Exception as e:
#             logger.error(f"❌ Failed to schedule advance for {fixture['home']} vs {fixture['away']}: {e}")
#             continue

#     logger.info(f"📊 Total advances scheduled: {scheduled_advances} out of {len(staging_fixtures)} fixtures")
#     return scheduled_advances

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
            
            # ✅ EXISTING METHOD: Pure delta detection (no side effects)
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

# ✅ UPDATE: fixtures_monitor_task - Trigger separate goal flow
@task(name="fixtures-monitor-task")
def fixtures_monitor_task():
    """Monitor active fixtures - trigger goal flows ONLY when goals actually changed"""
    logger = get_run_logger()
    
    delta_results = fixtures_delta_task()
    
    if delta_results["status"] != "success":
        logger.error("❌ Delta detection failed")
        return {"status": "error", "delta_results": delta_results}
    
    goal_flows_triggered = 0
    completed_fixtures_processed = 0
    
    # ✅ CRITICAL: Only process fixtures that ACTUALLY have goal changes
    for fixture_change in delta_results["fixtures_with_changes"]:
        fixture_id = fixture_change["fixture_id"]
        delta_result = fixture_change["delta_result"]
        
        # ✅ DOUBLE-CHECK: Only proceed if goals actually changed
        if not delta_result.get("goals_changed", False):
            logger.info(f"⚪ Fixture {fixture_id} - no goal changes, skipping goal flow")
            continue
        
        # ✅ ENHANCED: Log the specific goal change
        total_increase = delta_result.get("total_goal_increase", 0)
        current_goals = delta_result.get("current_goals", {})
        home_score = current_goals.get("home", 0)
        away_score = current_goals.get("away", 0)
        
        logger.info(f"🚨 GOAL DELTA DETECTED: Fixture {fixture_id} - +{total_increase} goals (now {home_score}-{away_score})")
        
        try:
            # Get complete goal events from API
            complete_goal_events = fixtures_events(fixture_id)
            
            if complete_goal_events:
                fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
                if fixture:
                    home_team = fixture.get("team_names", {}).get("home", "Home")
                    away_team = fixture.get("team_names", {}).get("away", "Away")
                    flow_run_name = f"⚽ GOALS: {home_team} {home_score}-{away_score} {away_team} - {len(complete_goal_events)} events [#{fixture_id}]"
                else:
                    flow_run_name = f"⚽ GOALS: Match #{fixture_id} - {home_score}-{away_score} - {len(complete_goal_events)} events"
                
                run_deployment(
                    name="fixtures-goal-flow/fixtures-goal-flow",
                    parameters={
                        "fixture_id": fixture_id,
                        "goal_events": complete_goal_events
                    },
                    flow_run_name=flow_run_name
                )
                
                goal_flows_triggered += 1
                logger.info(f"✅ Triggered goal flow: {flow_run_name}")
                
                # ✅ UPDATE: Update fixture with new scores immediately
                store.fixtures_update(fixture_id, delta_result)
                logger.info(f"✅ Updated fixture {fixture_id} with new scores: {home_score}-{away_score}")
                
            else:
                logger.warning(f"⚠️ No complete goal events found for fixture {fixture_id}")
            
        except Exception as e:
            logger.error(f"❌ Error triggering goal flow for fixture {fixture_id}: {e}")
            continue
    
    # Process completions (unchanged)
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"] 
        delta_result = completed_fixture["delta_result"]
        
        try:
            fixture = store.fixtures_active.find_one({"fixture_id": fixture_id})
            if fixture:
                home_team = fixture.get("team_names", {}).get("home", "Home")
                away_team = fixture.get("team_names", {}).get("away", "Away")
                home_score = delta_result.get("current_goals", {}).get("home", 0)
                away_score = delta_result.get("current_goals", {}).get("away", 0)
                status = delta_result.get("api_status", "FT")
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
                flow_run_name=flow_run_name
            )
            completed_fixtures_processed += 1
            logger.info(f"✅ Scheduled completion: {flow_run_name}")
            
        except Exception as e:
            logger.error(f"❌ Failed to process completion for fixture {fixture_id}: {e}")
    
    return {
        "status": "success",
        "active_fixtures": len(store.get_all_active_fixtures()),
        "goals_detected": delta_results["total_goals_detected"],
        "goal_flows_triggered": goal_flows_triggered,
        "completed_fixtures": completed_fixtures_processed,
        "delta_results": delta_results
    }

# ✅ FLOWS WITH CONSISTENT NAMING

@flow(name="fixtures-ingest-flow")
def fixtures_ingest_flow(date_str: Optional[str] = None, team_ids: Optional[str] = None):
    logger = get_run_logger()
    
    logger.info("📥 Starting Pure Fixtures Ingest Flow")
    populate_team_metadata(reset_first=False)
    params = fixtures_process_parameters_task(team_ids, date_str)
    team_fixtures = fixtures_fetch_api_task(params["query_date"], params["valid_team_ids"])
    if not team_fixtures:
        return {"status": "no_fixtures", "message": "No fixtures found"}
    categorized = fixtures_categorize_task(team_fixtures)
    storage_result = fixtures_store_task(
        categorized["staging_fixtures"], 
        categorized["active_fixtures"],
        categorized["completed_fixtures"]
    )

    # ✅ ALREADY CORRECT: Non-blocking scheduling using async client
    scheduled_advances = 0
    for fixture in categorized["staging_fixtures"]:
        kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
        advance_time = kickoff_time - timedelta(minutes=3)
        try:
            # ✅ PERFECT: Uses your proper async client approach
            result = schedule_advance(
                source="fixtures_staging",
                destination="fixtures_active",
                fixture_id=fixture["id"],
                scheduled_time=advance_time
            )
            if result.get("status") == "scheduled":
                scheduled_advances += 1
                logger.info(f"✅ Scheduled advance for: {fixture['home']} vs {fixture['away']} at {advance_time}")
        except Exception as e:
            logger.error(f"❌ Failed to schedule advance for {fixture['home']} vs {fixture['away']}: {e}")

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

# ✅ FIX: Use correct flow_run context
# ✅ CLEAN: fixtures_flows.py - Simple monitor, no naming
@flow(name="fixtures-monitor-flow")  
def fixtures_monitor_flow():
    """Monitor flow - uses default Prefect naming"""
    logger = get_run_logger()
    
    # ✅ REMOVE: All naming attempts - let Prefect use defaults
    
    if store.check_collections_empty(["fixtures_active"]):
        logger.info("⏸️ No active fixtures - skipping API calls")
        return {
            "status": "no_work_skipped", 
            "reason": "no_active_fixtures"
        }
    
    logger.info("🔍 Active fixtures found - performing monitoring")
    monitor_result = fixtures_monitor_task()
    
    return {
        "status": "work_completed", 
        "monitor_result": monitor_result
    }

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

# ✅ FIX: Complete the goal flow
@flow(name="fixtures-goal-flow")
def fixtures_goal_flow(fixture_id: int, goal_events: Optional[List[dict]] = None):
    """Dedicated goal processing flow - triggered by goal detection"""
    logger = get_run_logger()
    
    if not goal_events:
        logger.warning(f"⚠️ No goal events provided for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    logger.info(f"⚽ Processing {len(goal_events)} goal events for fixture {fixture_id}")
    
    goals_processed = []
    goals_failed = []
    
    for goal_event in goal_events:
        try:
            player_name = goal_event.get("player", {}).get("name", "")
            team_name = goal_event.get("team", {}).get("name", "")
            minute = goal_event.get("time", {}).get("elapsed", 0)
            
            if not player_name or not team_name or minute <= 0:
                logger.warning(f"⚠️ Skipping incomplete goal: player='{player_name}', team='{team_name}', minute={minute}")
                goals_failed.append(goal_event)
                continue
            
            # Store goal with validation
            goal_stored = store.store_goal_active(fixture_id, goal_event)
            
            if goal_stored:
                goals_processed.append(goal_event)
                logger.info(f"✅ Stored goal: {team_name} - {player_name} ({minute}')")
                
                # ✅ FIX: Actually trigger the automation events
                events_emitted = goal_trigger(fixture_id, [goal_event])
                logger.info(f"📡 Emitted {events_emitted} automation events")
            else:
                goals_failed.append(goal_event)
                logger.warning(f"⚠️ Failed to store goal: {team_name} - {player_name} ({minute}')")
                
        except Exception as e:
            logger.error(f"❌ Error processing goal: {e}")
            goals_failed.append(goal_event)
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "goals_processed": len(goals_processed),
        "goals_failed": len(goals_failed),
        "valid_goals": goals_processed
    }