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
    """Split fixtures into staging vs active based on kickoff time"""
    logger = get_run_logger()
    
    current_time = datetime.now(timezone.utc)
    staging_fixtures = []
    active_fixtures = []
    
    for fixture in team_fixtures:
        kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
        
        if kickoff_time > current_time:
            staging_fixtures.append(fixture)
        else:
            active_fixtures.append(fixture)
    
    logger.info(f"📊 Categorized: {len(staging_fixtures)} staging, {len(active_fixtures)} active")
    
    return {
        "staging_fixtures": staging_fixtures,
        "active_fixtures": active_fixtures
    }

@task(name="fixtures-schedule-advances-task", retries=3, retry_delay_seconds=10)
def fixtures_schedule_advances_task(staging_fixtures):
    """Schedule fixtures-advance-flow for each staging fixture with universal parameters"""
    logger = get_run_logger()
    
    scheduled_advances = 0
    
    for fixture in staging_fixtures:
        kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
        advance_time = kickoff_time - timedelta(minutes=3)  # ✅ CHANGED: 5 → 3 minutes
        
        logger.info(f"📅 SCHEDULING universal fixtures-advance-flow for fixture {fixture['id']} at {advance_time.strftime('%H:%M:%S')}")
        
        try:
            run_deployment(
                name="fixtures-advance-flow/fixtures-advance-flow",
                parameters={
                    "source_collection": "fixtures_staging",
                    "destination_collection": "fixtures_active",
                    "fixture_id": fixture["id"]
                },
                scheduled_time=advance_time,
                flow_run_name=f"Advance-{fixture['id']}-{kickoff_time.strftime('%H:%M')}"
            )
            scheduled_advances += 1
            logger.info(f"✅ SCHEDULED universal fixtures-advance-flow for fixture {fixture['id']}")
            
        except Exception as e:
            logger.error(f"❌ Failed to schedule fixtures-advance-flow for fixture {fixture['id']}: {e}")
    
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

@task(name="fixtures-active-goals-task")
def fixtures_active_goals_task(active_fixtures):
    """Process existing goals for active fixtures"""
    logger = get_run_logger()
    
    total_goals_processed = 0
    
    for fixture in active_fixtures:
        try:
            api_goal_events = fixtures_events(fixture["id"])
            if api_goal_events:
                goals_stored = store.goals_update(fixture["id"], api_goal_events)
                goal_trigger(fixture["id"], api_goal_events)
                total_goals_processed += goals_stored
                logger.info(f"⚽ Processed {goals_stored} goals for fixture {fixture['id']}")
        except Exception as e:
            logger.error(f"❌ Error processing goals for fixture {fixture['id']}: {e}")
    
    logger.info(f"✅ Total goals processed: {total_goals_processed}")
    return total_goals_processed

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
    
    # ✅ PROCESS COMPLETIONS: Handle completed fixtures AFTER goals processed
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"]
        delta_result = completed_fixture["delta_result"]
        
        try:
            # ✅ CRITICAL: Process any final goals BEFORE moving to completed
            if delta_result.get("goals_changed", False):
                logger.info(f"🚨 Processing final goals before completion for fixture {fixture_id}")
                final_change_result = store.handle_fixture_changes(fixture_id, delta_result)
                final_goals = final_change_result.get("goals_processed", 0)
                total_goals_processed += final_goals
                logger.info(f"⚽ Processed {final_goals} final goals before completion")
            
            # ✅ ADVANCE COMPLETED: Call universal advance flow
            logger.info(f"📋 Calling fixtures-advance-flow for completed fixture {fixture_id}")
            
            run_deployment(
                name="fixtures-advance-flow/fixtures-advance-flow",
                parameters={
                    "source_collection": "fixtures_active",
                    "destination_collection": "fixtures_processed", 
                    "fixture_id": fixture_id
                },
                timeout=0,  # Run immediately
                flow_run_name=f"Complete-{fixture_id}-{datetime.now(timezone.utc).strftime('%H:%M:%S')}"
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

@flow(name="fixtures-ingest-flow")  # ✅ CONSISTENT NAMING
def fixtures_ingest_flow(date_str: Optional[str] = None, team_ids: Optional[str] = None):
    """Multi-task ingest flow - completely decoupled from monitoring"""
    logger = get_run_logger()
    logger.info("📥 Starting Fixtures Ingest Flow")
    
    # Initialize team metadata (keep this here as it's a prerequisite)
    populate_team_metadata(reset_first=False)
    
    # Task 1: Process and validate parameters
    params = fixtures_process_parameters_task(team_ids, date_str)
    
    # Task 2: Fetch fixtures from API
    team_fixtures = fixtures_fetch_api_task(params["query_date"], params["valid_team_ids"])
    
    # Early return if no fixtures
    if not team_fixtures:
        return {
            "status": "success",
            "team_fixtures": 0,
            "staging_fixtures": 0,
            "active_fixtures": 0,
            "goals_processed": 0,
            "scheduled_advances": 0,
            "monitoring_started": False
        }
    
    # Task 3: Categorize fixtures by time
    categorized = fixtures_categorize_task(team_fixtures)
    
    # Task 4: Schedule advance flows for staging fixtures
    scheduled_advances = fixtures_schedule_advances_task(categorized["staging_fixtures"])
    
    # Task 5: Store fixtures in database
    storage_result = fixtures_store_bulk_task(categorized["staging_fixtures"], categorized["active_fixtures"])
    
    # Task 6: Process existing goals for active fixtures
    goals_processed = fixtures_active_goals_task(categorized["active_fixtures"])
    
    logger.info(f"✅ Ingest complete: {storage_result['active_count']} active, {scheduled_advances} advances scheduled")
    
    return {
        "status": "success",
        "team_fixtures": len(team_fixtures),
        "staging_fixtures": storage_result["staging_count"],
        "active_fixtures": storage_result["active_count"],
        "goals_processed": goals_processed,
        "scheduled_advances": scheduled_advances,
        "monitoring_status": "handled_by_cron_schedule",  # ✅ NEW
        "completed_at": datetime.now(timezone.utc).isoformat()
    }

@flow(name="fixtures-advance-flow")  # ✅ CONSISTENT NAMING
def fixtures_advance_flow(
    source_collection: str = "fixtures_staging", 
    destination_collection: str = "fixtures_active",
    fixture_id: Optional[int] = None
):
    """
    🚀 UNIVERSAL FIXTURE ADVANCEMENT FLOW
    
    Can be used for:
    - Scheduled advancement: fixtures_staging → fixtures_active (at kickoff-5min)
    - Immediate completion: fixtures_active → fixtures_processed (when completed)
    """
    logger = get_run_logger()
    logger.info(f"📋 Universal advancement: {source_collection} → {destination_collection}")
    
    if fixture_id:
        logger.info(f"🎯 Processing specific fixture: {fixture_id}")
    
    # Use the universal task
    advance_result = fixtures_advance_task(source_collection, destination_collection, fixture_id)
    
    # ✅ SMART POST-PROCESSING: Different actions based on destination
    if advance_result["status"] == "success" and advance_result["advanced_count"] > 0:
        
        # 🎯 STAGING → ACTIVE: Check for existing goals
        if destination_collection == "fixtures_active" and fixture_id:
            logger.info(f"✅ Fixture {fixture_id} promoted to active - checking for existing goals")
            try:
                api_goal_events = fixtures_events(fixture_id)
                if api_goal_events:
                    goals_stored = store.goals_update(fixture_id, api_goal_events)
                    goal_trigger(fixture_id, api_goal_events)
                    logger.info(f"⚽ Processed {goals_stored} existing goals")
            except Exception as e:
                logger.error(f"❌ Error checking goals: {e}")
        
        # 🏁 ACTIVE → PROCESSED: Log completion
        elif destination_collection == "fixtures_processed":
            logger.info(f"🏁 Completed fixture advancement to processed collection")
        
        # 📊 OTHER: Generic logging
        else:
            logger.info(f"✅ Generic advancement completed: {advance_result['advanced_count']} fixtures")
    
    return {
        "status": advance_result["status"],
        "source_collection": source_collection,
        "destination_collection": destination_collection,
        "fixture_id": fixture_id,
        "advanced_count": advance_result.get("advanced_count", 0),
        "advance_result": advance_result
    }

@flow(name="fixtures-monitor-flow")
def fixtures_monitor_flow():
    """Always-running monitor - runs every 3 minutes, skips API calls when no work"""
    logger = get_run_logger()
    
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