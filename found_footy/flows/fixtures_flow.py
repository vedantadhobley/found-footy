from datetime import datetime, timedelta
from prefect import flow, task, get_run_logger
from prefect.events import emit_event
import pendulum
import time
from typing import Optional
from found_footy.api.mongo_api import (
    get_fixtures_by_date,  # ✅ CHANGED: Use team-based function
    get_fixture_details,
    get_fixture_events,
    populate_team_metadata,  # ✅ CHANGED: Use team metadata
    store_fixture_result,
    store_fixture_events,
    parse_team_ids_parameter,  # ✅ CHANGED: Parse team IDs
    get_available_teams  # ✅ CHANGED: Get teams instead of leagues
)

# ✅ Import store directly for one-liner methods we removed
from found_footy.storage.mongo_store import FootyMongoStore

# Create store instance for direct access
store = FootyMongoStore()

@task(name="fetch-fixtures-by-teams")
def fetch_fixtures_by_teams(team_ids=None, date_str=None):
    """Fetch fixtures by team IDs or all top 25 teams"""
    logger = get_run_logger()
    
    if date_str:
        query_date = datetime.strptime(date_str, "%Y%m%d").date()
    else:
        from datetime import timezone
        query_date = datetime.now(timezone.utc).date()
    
    # ✅ FIXED: Handle empty string as well as None
    if team_ids and team_ids.strip():  # Check for non-empty string
        # Parse team IDs if provided
        if isinstance(team_ids, str):
            team_ids = parse_team_ids_parameter(team_ids)
        
        logger.info(f"⚽ Fetching fixtures for specific teams {team_ids} on {query_date}")
        
        # Get available teams for validation
        available_teams = get_available_teams()
        
        valid_team_ids = []
        for team_id in team_ids:
            if team_id in available_teams:
                valid_team_ids.append(team_id)
                logger.info(f"  ⚽ {team_id}: {available_teams[team_id]['name']} ({available_teams[team_id]['country']})")
            else:
                logger.warning(f"  ⚠️ Unknown team ID: {team_id}")
        
        if not valid_team_ids:
            logger.error("❌ No valid team IDs provided")
            return []
        
        # Use the specific API function for teams
        from found_footy.api.mongo_api import get_fixtures_by_teams
        fixtures = get_fixtures_by_teams(valid_team_ids, query_date)
    else:
        # Use all top 25 teams (when team_ids is None, empty string, or whitespace)
        logger.info(f"⚽ Fetching fixtures for ALL top 25 UEFA teams on {query_date}")
        fixtures = get_fixtures_by_date(query_date)
    
    if fixtures:
        logger.info(f"✅ Found {len(fixtures)} fixtures:")
        for fixture in fixtures:
            logger.info(f"  📅 {fixture['id']}: {fixture['home']} vs {fixture['away']} at {fixture['time']} ({fixture['league']})")
    else:
        logger.warning(f"❌ No fixtures found for top 25 teams on {query_date}")
    
    return fixtures

def check_fixture_status_sync(fixture_id):
    """Check fixture status - direct API call"""
    detailed_data = get_fixture_details(fixture_id)
    
    if not detailed_data:
        return None, "No data found", "No data"
    
    fixture = detailed_data[0]
    status = fixture.get("fixture", {}).get("status", {}).get("short")
    home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
    away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
    home_goals = fixture.get("goals", {}).get("home")
    away_goals = fixture.get("goals", {}).get("away")
    
    finished_statuses = {"FT", "AET", "PEN"}
    dropped_statuses = {"CANC", "ABD", "AWD", "WO", "PST"}
    
    if status in finished_statuses:
        return "COMPLETED", status, f"{home_team} {home_goals} - {away_goals} {away_team}", fixture
    elif status in dropped_statuses:
        return "DROPPED", status, f"{home_team} vs {away_team}", fixture
    else:
        score_info = ""
        if home_goals is not None and away_goals is not None:
            score_info = f" ({home_team} {home_goals} - {away_goals} {away_team})"
        return "IN_PROGRESS", status, f"{home_team} vs {away_team}{score_info}", fixture

@task(name="monitor-fixture", retries=100, retry_delay_seconds=300)
def monitor_fixture_sync(fixture_id, fixture_start_time):
    """Monitor fixture until completion"""
    logger = get_run_logger()
    
    # Calculate when to start monitoring (2 hours AFTER kick-off)
    start_monitoring_time = pendulum.parse(fixture_start_time).add(hours=2)
    current_time = pendulum.now("UTC")
    
    # If it's not time to start monitoring yet, wait
    if current_time < start_monitoring_time:
        wait_seconds = (start_monitoring_time - current_time).total_seconds()
        logger.info(f"⏰ Waiting {wait_seconds/60:.1f} minutes until 2 hours after kick-off for fixture {fixture_id}")
        time.sleep(wait_seconds)
    
    logger.info(f"🔍 Checking fixture {fixture_id} status...")
    
    # Check fixture status
    result, status, match_info, fixture_data = check_fixture_status_sync(fixture_id)
    
    if result == "COMPLETED":
        logger.info(f"🎉 Fixture {fixture_id} completed: {match_info}")
        return {
            "fixture_id": fixture_id, 
            "status": status, 
            "result": "COMPLETED", 
            "match_info": match_info,
            "fixture_data": fixture_data
        }
    elif result == "DROPPED":
        logger.warning(f"🚫 Fixture {fixture_id} dropped: {match_info}")
        return {
            "fixture_id": fixture_id, 
            "status": status, 
            "result": "DROPPED", 
            "match_info": match_info,
            "fixture_data": fixture_data
        }
    else:
        logger.info(f"⏳ Fixture {fixture_id} still in progress ({status}) - will retry in 5 minutes")
        raise Exception(f"Fixture {fixture_id} not yet completed (status: {status})")

@task(name="store-and-emit")
def store_and_emit_task(fixture_id, status, match_info, fixture_data):
    """Store fixture result and emit event"""
    logger = get_run_logger()
    
    logger.info(f"💾 Storing result for fixture {fixture_id}: {match_info}")
    
    try:
        # Store fixture result
        success = store_fixture_result(fixture_data)
        
        # Store events
        try:
            events_data = get_fixture_events(fixture_id)
            if events_data:
                goal_events = store_fixture_events(fixture_id, events_data)
                logger.info(f"⚽ Stored {goal_events} goal events")
        except Exception as e:
            logger.warning(f"⚠️ Error storing events: {e}")
        
        # ✅ CHANGED: Emit event for Twitter automation instead of YouTube
        emit_event(
            event="fixture.completed",
            resource={"prefect.resource.id": f"fixture.{fixture_id}"},
            payload={
                "fixture_id": fixture_id,
                "home_team": fixture_data.get("teams", {}).get("home", {}).get("name"),
                "away_team": fixture_data.get("teams", {}).get("away", {}).get("name"),
                "status": fixture_data.get("fixture", {}).get("status", {}).get("short"),
                "league_id": fixture_data.get("league", {}).get("id"),
                "league_name": fixture_data.get("league", {}).get("name"),
                "completed_at": datetime.now().isoformat(),
                "parent_flow_run_id": logger.extra.get("flow_run_id"),
                "match_info": match_info,
                "trace_id": f"fixture-{fixture_id}-{datetime.now().strftime('%Y%m%d-%H%M%S')}"
            }
        )
        
        logger.info(f"📡 ✅ EMITTED fixture.completed event for {fixture_id}")
        logger.info(f"🐦 Twitter flow will start with trace_id: fixture-{fixture_id}-{datetime.now().strftime('%Y%m%d-%H%M%S')}")
        
        return {
            "fixture_id": fixture_id,
            "stored": success,
            "event_emitted": True,
            "trace_id": f"fixture-{fixture_id}-{datetime.now().strftime('%Y%m%d-%H%M%S')}",
            "twitter_trigger": "automated"
        }
        
    except Exception as e:
        logger.error(f"❌ Error in store and emit: {e}")
        return {"fixture_id": fixture_id, "stored": False, "event_emitted": False, "error": str(e)}

@task(name="monitor-single-fixture")
def monitor_single_fixture_task(fixture):
    """Monitor single fixture and trigger Twitter on completion"""
    fixture_id = fixture["id"]
    fixture_time = fixture["time"]
    home_team = fixture["home"]
    away_team = fixture["away"]
    
    logger = get_run_logger()
    logger.info(f"🏁 Starting fixture monitor: {fixture_id} - {home_team} vs {away_team}")
    
    try:
        # Step 1: Monitor until completion
        monitor_result = monitor_fixture_sync(fixture_id, fixture_time)
        
        if monitor_result["result"] == "COMPLETED":
            logger.info(f"✅ Fixture {fixture_id} completed - storing and triggering Twitter")
            
            # Step 2: Store and emit event (triggers Twitter flow)
            store_result = store_and_emit_task(
                monitor_result["fixture_id"],
                monitor_result["status"], 
                monitor_result["match_info"],
                monitor_result["fixture_data"]
            )
            
            logger.info(f"🚀 Task complete for {fixture_id} - Twitter flow should be running independently!")
            
            return {
                "fixture_id": fixture_id,
                "status": "completed_and_triggered_twitter",
                "monitor_result": monitor_result,
                "store_result": store_result
            }
            
        else:
            logger.info(f"⚠️ Fixture {fixture_id} was {monitor_result['result']} - no Twitter trigger needed")
            return {
                "fixture_id": fixture_id,
                "status": f"completed_as_{monitor_result['result'].lower()}",
                "monitor_result": monitor_result,
                "store_result": None
            }
            
    except Exception as e:
        logger.error(f"❌ Monitor task failed for {fixture_id}: {e}")
        return {
            "fixture_id": fixture_id,
            "status": "failed",
            "error": str(e)
        }

@flow(name="fixtures-flow")
def fixtures_flow(date_str: Optional[str] = None, team_ids: str = ""):
    """
    Team-based fixtures monitoring with MongoDB reset on startup
    
    Parameters:
    - date_str: Date in YYYYMMDD format (optional, defaults to today)
    - team_ids: JSON array or comma-separated string of team IDs (optional, defaults to all top 25)
                Examples: "[541,50,42]" (Real Madrid, Man City, Liverpool)
                         "541,50,42" (comma-separated)
                         "" (empty string = all top 25 teams)
    """
    logger = get_run_logger()
    logger.info("🚀 Starting Found Footy Fixtures Flow (Team-based - Top 25 UEFA)")
    logger.info("🎯 Each fixture will be monitored independently")
    logger.info("📡 Completed fixtures will trigger Twitter flows via events")
    
    # Parse team IDs (if provided and not empty)
    parsed_team_ids = None
    if team_ids and team_ids.strip():  # ✅ FIXED: Check for non-empty string
        try:
            parsed_team_ids = parse_team_ids_parameter(team_ids)
            logger.info(f"⚽ Target teams: {parsed_team_ids}")
        except Exception as e:
            logger.error(f"❌ Error parsing team_ids parameter: {e}")
            logger.info("🔄 Using all top 25 teams instead")
    else:
        logger.info("⚽ Using all top 25 UEFA teams")
    
    # Reset MongoDB and populate team metadata
    logger.info("🗑️ Resetting MongoDB and initializing team metadata...")
    populate_team_metadata(reset_first=True)
    logger.info("✅ MongoDB reset and team initialization complete")
    
    # Determine the target date for this run
    if date_str:
        target_date = date_str
        logger.info(f"📅 Running for specified date: {target_date}")
    else:
        from datetime import timezone
        today = datetime.now(timezone.utc).date()
        target_date = today.strftime("%Y%m%d")
        logger.info(f"📅 Running for today's date: {target_date}")
    
    # Fetch fixtures for the target date and teams
    fixtures = fetch_fixtures_by_teams(parsed_team_ids, target_date)
    
    if not fixtures:
        logger.warning("❌ No fixtures found for the specified date and teams")
        return {
            "fixtures_monitored": 0,
            "target_date": target_date,
            "team_ids": parsed_team_ids or "all_top_25",
            "results": []
        }
    
    logger.info(f"🎯 Starting {len(fixtures)} independent fixture monitors")
    
    # Fire-and-forget pattern - don't wait for results
    task_futures = []
    for fixture in fixtures:
        future = monitor_single_fixture_task.submit(fixture)
        task_futures.append(future)
        logger.info(f"🏁 Launched monitor for {fixture['id']}: {fixture['home']} vs {fixture['away']} ({fixture['league']})")
    
    logger.info("🔥 ALL FIXTURE MONITORS LAUNCHED!")
    logger.info("📡 Flow exiting - tasks will emit events when fixtures complete")
    logger.info("🐦 Twitter flows will start automatically via automation")
    
    # Return immediately - let tasks run independently
    return {
        "fixtures_monitored": len(fixtures),
        "tasks_launched": len(task_futures),
        "target_date": target_date,
        "team_ids": parsed_team_ids or "all_top_25",
        "processing_type": "team_based_monitoring_with_event_triggers",
        "flow_completed_at": datetime.now().isoformat(),
        "note": "Fixture monitors running independently - Twitter flows triggered via events"
    }