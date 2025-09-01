from datetime import datetime, timedelta, timezone
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
import pendulum
import time  # ✅ Add this missing import
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
from found_footy.flows.twitter_flow import twitter_flow
from found_footy.storage.mongo_store import FootyMongoStore

# Create store instance
store = FootyMongoStore()

# ✅ HELPER METHOD: fixtures_advance
def fixtures_advance(source_collection: str, destination_collection: str) -> int:
    """Helper method: Copy fixtures from source to destination collection"""
    logger = get_run_logger()
    logger.info(f"📋 Advancing fixtures from {source_collection} to {destination_collection}")
    
    count = store.fixtures_advance(source_collection, destination_collection)
    logger.info(f"✅ Advanced {count} fixtures")
    return count

# ✅ HELPER METHOD: goal_trigger - Enhanced with timeline data
def goal_trigger(fixture_id: int, new_goal_events: List[dict] = None):
    """Helper method: Emit goal events with enhanced timeline data for automation"""
    logger = get_run_logger()
    logger.info(f"⚽ Goal trigger for fixture {fixture_id}")
    
    try:
        if new_goal_events is None:
            logger.warning(f"⚠️ No goal events provided to goal_trigger for fixture {fixture_id}")
            return 0
        
        events_emitted = 0
        current_time = datetime.now(timezone.utc)
        
        for goal_event in new_goal_events:
            minute = goal_event.get("time", {}).get("elapsed", 0)
            player_id = goal_event.get("player", {}).get("id", 0)
            goal_id = f"{fixture_id}_{minute}_{player_id}"
            
            # Extract team and player names for timeline
            team_name = goal_event.get("team", {}).get("name", "Unknown Team")
            player_name = goal_event.get("player", {}).get("name", "Unknown Player")
            goal_type = goal_event.get("detail", "Goal")
            
            # ✅ TIMELINE: Enhanced event with rich data for custom flow run names
            logger.info(f"📡 Emitting ENHANCED goal.detected event for goal {goal_id}")
            try:
                from prefect.events import emit_event
                
                emit_event(
                    event="goal.detected",
                    resource={"prefect.resource.id": f"goal.{goal_id}"},
                    payload={
                        "goal_id": goal_id,  # ✅ Required for automation parameter
                        "fixture_id": fixture_id,
                        "team_name": team_name,  # ✅ Enhanced: Clean team name for flow run
                        "player_name": player_name,  # ✅ Enhanced: Clean player name for flow run  
                        "minute": minute,  # ✅ Enhanced: Minute for flow run
                        "goal_type": goal_type,
                        "detected_at": current_time.isoformat(),  # ✅ Timeline: Detection timestamp
                        "detection_time": current_time.strftime('%H:%M:%S'),  # ✅ Timeline: Time only for flow run
                        # ✅ Enhanced: Additional context for timeline
                        "team_short": team_name.replace(" ", "").replace("FC", "").replace("United", "Utd")[:10],  # Shortened team name
                        "player_short": player_name.split()[-1] if " " in player_name else player_name,  # Last name only
                        "is_penalty": "Penalty" in goal_type,
                        "is_own_goal": "Own Goal" in goal_type
                    }
                )
                events_emitted += 1
                
                # ✅ Timeline logging with rich details
                logger.info(f"✅ TIMELINE EVENT: {current_time.strftime('%H:%M:%S')} | {team_name} | {player_name} ({minute}') | {goal_type}")
                logger.info(f"🎯 Expected flow run name: ⚽ {team_name}-{player_name}-{minute}min-{current_time.strftime('%H:%M:%S')}")
                
            except ImportError:
                logger.error("❌ emit_event not available - automation will NOT work")
                return 0
            except Exception as e:
                logger.error(f"❌ Failed to emit event for goal {goal_id}: {e}")
                return 0
        
        # ✅ Timeline summary
        if events_emitted > 0:
            logger.info(f"📊 TIMELINE SUMMARY: {events_emitted} goals detected for fixture {fixture_id} at {current_time.strftime('%H:%M:%S')}")
        
        logger.info(f"✅ Goal trigger complete: {events_emitted} events emitted for automation")
        return events_emitted
        
    except Exception as e:
        logger.error(f"❌ Error in goal_trigger for fixture {fixture_id}: {e}")
        return 0

@task(name="fixtures-ingest")
def fixtures_ingest(team_ids=None, date_str=None):
    """Ingest fixtures from API into staging/active collections based on timing"""
    logger = get_run_logger()
    
    # ✅ FIXED: Proper null handling for date_str
    if date_str is None or date_str == "":
        query_date = datetime.now(timezone.utc).date()
    else:
        try:
            query_date = datetime.strptime(date_str[:8], "%Y%m%d").date() if len(date_str) == 8 else datetime.strptime(date_str, "%Y-%m-%d").date()
        except (ValueError, TypeError):
            logger.warning(f"⚠️ Invalid date_str '{date_str}', using today")
            query_date = datetime.now(timezone.utc).date()
    
    logger.info(f"📅 Ingesting fixtures for date: {query_date}")
    
    # ✅ FIXED: Proper null handling for team_ids
    available_team_ids = store.get_team_ids()
    valid_team_ids = available_team_ids  # Default to all teams
    
    if team_ids is not None and str(team_ids).strip() and str(team_ids).strip() != "null":
        try:
            if isinstance(team_ids, str):
                if team_ids.startswith('[') and team_ids.endswith(']'):
                    import json
                    target_team_ids = json.loads(team_ids)
                else:
                    target_team_ids = [int(x.strip()) for x in team_ids.split(",") if x.strip()]
            elif isinstance(team_ids, (list, tuple)):
                target_team_ids = [int(x) for x in team_ids]
            elif isinstance(team_ids, int):
                target_team_ids = [team_ids]
            else:
                target_team_ids = available_team_ids
            
            valid_team_ids = [tid for tid in target_team_ids if tid in available_team_ids]
            
            if not valid_team_ids:
                logger.warning("⚠️ No valid teams found, using all available teams")
                valid_team_ids = available_team_ids
                
        except Exception as e:
            logger.error(f"❌ Error parsing team_ids '{team_ids}': {e}")
            valid_team_ids = available_team_ids
    
    logger.info(f"⚽ Using {len(valid_team_ids)} teams")
    
    # ✅ API calls
    all_fixtures = fixtures(query_date)
    team_fixtures = filter_fixtures_by_teams(all_fixtures, valid_team_ids)
    
    if not team_fixtures:
        logger.warning("❌ No fixtures found for selected teams")
        return {"total_fixtures": len(all_fixtures), "team_fixtures": 0, "staging_fixtures": 0, "active_fixtures": 0}
    
    logger.info(f"✅ Found {len(team_fixtures)} fixtures for our teams")
    
    # ✅ Categorize fixtures
    current_time = datetime.now(timezone.utc)
    staging_fixtures = []
    active_fixtures = []
    
    for fixture in team_fixtures:
        kickoff_time = datetime.fromisoformat(fixture["time"].replace('Z', '+00:00'))
        if kickoff_time > current_time:
            staging_fixtures.append(fixture)
        else:
            active_fixtures.append(fixture)
    
    logger.info(f"📋 Staging: {len(staging_fixtures)}, Active: {len(active_fixtures)}")
    
    # ✅ Bulk operations
    staging_count = store.bulk_insert_fixtures_staging(staging_fixtures) if staging_fixtures else 0
    active_count = store.bulk_insert_fixtures_active(active_fixtures) if active_fixtures else 0
    
    # ✅ CLEANER: Process goals for active fixtures using delta/update pattern
    total_goals_processed = 0
    for fixture in active_fixtures:
        api_goal_events = fixtures_events(fixture["id"])
        if api_goal_events:
            # For fresh fixtures, ALL API goals are "new" (empty vs API = all new)
            goals_stored = store.goals_update(fixture["id"], api_goal_events)
            goal_trigger(fixture["id"], api_goal_events)
            total_goals_processed += goals_stored
    
    if total_goals_processed > 0:
        logger.info(f"⚽ Processed {total_goals_processed} goals during ingest")
    
    return {
        "total_fixtures": len(all_fixtures),
        "team_fixtures": len(team_fixtures),
        "staging_fixtures": staging_count,
        "active_fixtures": active_count,
        "goals_processed": total_goals_processed
    }

@task(name="fixtures-monitor")
def fixtures_monitor():
    """Monitor active fixtures for goal changes and completion"""
    logger = get_run_logger()
    
    active_fixtures = store.get_all_active_fixtures()
    if not active_fixtures:
        logger.info("⏸️ No active fixtures to monitor")
        return {"active_fixtures": 0, "goals_detected": 0, "completed_fixtures": 0}
    
    fixture_ids = [f["fixture_id"] for f in active_fixtures]
    logger.info(f"🔍 Monitoring {len(fixture_ids)} fixtures")
    
    try:
        # ✅ CLEANER: Batch API call
        api_fixtures_data = fixtures_batch(fixture_ids)
        api_lookup = {f["fixture"]["id"]: f for f in api_fixtures_data}
        
        goals_detected = 0
        completed_fixtures = 0
        completed_fixture_ids = []
        
        # ✅ SIMPLIFIED: fixtures_delta handles everything internally
        for active_fixture in active_fixtures:
            fixture_id = active_fixture["fixture_id"]
            api_data = api_lookup.get(fixture_id)
            
            if not api_data:
                continue
            
            # 🔍 SINGLE CALL: fixtures_delta handles goals_delta + goals_update + goal_trigger + fixtures_update
            fixtures_delta_result = store.fixtures_delta(fixture_id, api_data)
            
            # 📊 TRACK RESULTS: Just collect stats, no additional processing needed
            if fixtures_delta_result.get("goals_changed", False):
                new_goals_count = len(fixtures_delta_result.get("new_goal_events", []))
                goals_detected += new_goals_count
                logger.info(f"⚽ {new_goals_count} new goals processed for fixture {fixture_id}")
            
            # 📋 TRACK COMPLETION: For moving to processed collection
            if fixtures_delta_result.get("fixture_completed", False):
                completed_fixtures += 1
                completed_fixture_ids.append(fixture_id)
        
        # ✅ MOVE COMPLETED: Only thing monitor does besides calling fixtures_delta
        for completed_id in completed_fixture_ids:
            try:
                store.fixtures_advance("fixtures_active", "fixtures_processed", completed_id)
            except Exception as e:
                logger.warning(f"⚠️ Failed to move fixture {completed_id}: {e}")
        
        logger.info(f"✅ Monitor: {goals_detected} goals, {completed_fixtures} completed")
        
        return {
            "active_fixtures": len(active_fixtures),
            "goals_detected": goals_detected,
            "completed_fixtures": completed_fixtures
        }
        
    except Exception as e:
        logger.error(f"❌ Monitor failed: {e}")
        return {"active_fixtures": len(active_fixtures), "goals_detected": 0, "completed_fixtures": 0, "error": str(e)}

@flow(name="fixtures-flow")
def fixtures_flow(date_str: Optional[str] = None, team_ids: Optional[str] = None):
    """✅ FIXED: Consistent null handling with proper type hints"""
    logger = get_run_logger()
    logger.info("🚀 Starting Restructured Fixtures Flow")
    
    # ✅ CONSISTENT: Only check for None/null, not empty strings
    if date_str is None:
        current_date = datetime.now(timezone.utc).date()
        date_str_to_use = current_date.strftime("%Y%m%d")
        display_date = current_date.strftime("%Y-%m-%d")
        logger.info(f"📅 Using today's date: {display_date} (auto-generated)")
    else:
        date_str_to_use = date_str
        if len(date_str) == 8:  # YYYYMMDD format
            display_date = f"{date_str[:4]}-{date_str[4:6]}-{date_str[6:8]}"
        else:
            display_date = date_str
        logger.info(f"📅 Using provided date: {display_date}")
    
    logger.info(f"📋 Parameters received:")
    logger.info(f"  date_str: {date_str} → using: {date_str_to_use}")
    logger.info(f"  team_ids: {team_ids} (type: {type(team_ids).__name__})")
    
    # Initialize team metadata
    logger.info("⚽ Ensuring team metadata...")
    populate_team_metadata(reset_first=False)
    logger.info("✅ Team metadata ready")
    
    # ✅ Step 1: Fixtures ingest
    logger.info("📥 Step 1: Running fixtures ingest...")
    ingest_result = fixtures_ingest(team_ids, date_str_to_use)  # Use processed date string
    
    # ✅ Step 2: Start monitoring (every 3 minutes, starting immediately)
    logger.info("🔄 Step 2: Starting fixtures monitor (every 3 minutes)...")
    
    monitor_cycles = 0
    max_cycles = 480  # 24 hours worth of 3-minute cycles
    
    while monitor_cycles < max_cycles:
        monitor_cycles += 1
        cycle_start = pendulum.now()
        
        logger.info(f"🔄 Monitor cycle #{monitor_cycles} - {cycle_start.format('HH:mm:ss')}")
        
        # Run monitoring
        monitor_result = fixtures_monitor()
        
        # Check if we should continue
        active_count = monitor_result.get("active_fixtures", 0)
        
        # Check if both staging and active are empty
        if store.check_collections_empty(["fixtures_staging", "fixtures_active"]):
            logger.info("✅ Both staging and active collections are empty - ending monitoring")
            break
        
        if active_count == 0:
            logger.info("⏸️ No active fixtures, but checking if staging has fixtures...")
            
            # Check if staging has fixtures that might be promoted soon
            staging_count = store.fixtures_staging.count_documents({})
            if staging_count == 0:
                logger.info("✅ No staging fixtures either - ending monitoring")
                break
            else:
                logger.info(f"⏳ {staging_count} fixtures in staging - continuing monitoring")
        
        # ✅ FIXED: Always wait 3 minutes between cycles (no immediate second run)
        logger.info("⏱️ Waiting 3 minutes before next monitor cycle...")
        time.sleep(180)  # 3 minutes
    
    logger.info(f"🏁 Fixtures flow completed after {monitor_cycles} cycles")
    
    return {
        "ingest_result": ingest_result,
        "monitor_cycles": monitor_cycles,
        "completed_at": pendulum.now().isoformat()
    }