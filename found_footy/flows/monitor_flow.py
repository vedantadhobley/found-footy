# ✅ NEW: found_footy/flows/monitor_flow.py
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from typing import List

from found_footy.flows.shared_tasks import fixtures_delta_task, store
from found_footy.api.mongo_api import fixtures_events
from found_footy.flows.flow_naming import generate_monitor_flow_name

@task(name="fixtures-monitor-task")
def fixtures_monitor_task():
    """Monitor active fixtures - trigger goal flows ONLY when goals actually changed"""
    logger = get_run_logger()
    
    delta_results = fixtures_delta_task()
    
    if delta_results["status"] != "success":
        logger.error("❌ Delta detection failed")
        return {"status": "error", "delta_results": delta_results}
    
    goal_flows_triggered = 0
    completed_fixtures_count = 0
    
    # Track fixtures with incomplete goals
    fixtures_with_incomplete_goals = set()
    
    # Process fixtures with goal changes
    for fixture_change in delta_results["fixtures_with_changes"]:
        fixture_id = fixture_change["fixture_id"]
        delta_result = fixture_change["delta_result"]
        
        if not delta_result.get("goals_changed", False):
            logger.info(f"⚪ Fixture {fixture_id} - no goal changes, skipping goal flow")
            continue
        
        total_increase = delta_result.get("total_goal_increase", 0)
        current_goals = delta_result.get("current_goals", {})
        home_score = current_goals.get("home", 0)
        away_score = current_goals.get("away", 0)
        
        logger.info(f"🚨 GOAL DELTA DETECTED: Fixture {fixture_id} - +{total_increase} goals (now {home_score}-{away_score})")
        
        # Fetch actual EVENTS data from API
        try:
            events_list = fixtures_events(fixture_id)  # ✅ Returns raw events array
            complete_goal_events = []
            
            # ✅ FIX: Process events directly - no need to check fixture.id
            for event in events_list:
                if (event.get("type") == "Goal" and 
                    event.get("detail") != "Missed Penalty" and
                    event.get("player", {}).get("name")):  # Only complete goals
                    complete_goal_events.append(event)
            
            if complete_goal_events:
                # COMPLETE GOALS: Process them AND update fixture
                fixture = store.fixtures_active.find_one({"_id": fixture_id})
                if fixture:
                    home_team, away_team = store._extract_team_names(fixture)
                    flow_run_name = f"⚽ GOALS: {home_team} {home_score}-{away_score} {away_team} - {len(complete_goal_events)} events [#{fixture_id}]"
                else:
                    flow_run_name = f"⚽ GOALS: Match #{fixture_id} - {home_score}-{away_score} - {len(complete_goal_events)} events"

                run_deployment(
                    name="goal-flow/goal-flow",
                    parameters={
                        "fixture_id": fixture_id,
                        "goal_events": complete_goal_events
                    },
                    flow_run_name=flow_run_name
                )
                
                goal_flows_triggered += 1
                logger.info(f"✅ Triggered goal flow: {flow_run_name}")
                
                # UPDATE: Only update fixture when goals are complete - CLEAN UPDATE
                # ❌ REMOVE: Don't store delta results in fixture
                # store.fixtures_update(fixture_id, delta_result)  # This adds extra fields
                
                # ✅ FIX: Update only the core fixture data from API
                current_fixture = store.fixtures_active.find_one({"_id": fixture_id})
                if current_fixture:
                    # Keep only core API fields + updated goals/score
                    clean_update = {
                        "goals": delta_result.get("current_goals", {}),
                        "score": delta_result.get("current_score", {})
                    }
                    store.fixtures_active.update_one(
                        {"_id": fixture_id}, 
                        {"$set": clean_update}
                    )
                    logger.info(f"✅ Updated fixture {fixture_id} with new scores: {home_score}-{away_score}")
                
            else:
                # ✅ INCOMPLETE GOALS: Skip fixture update AND mark as incomplete
                logger.warning(f"⚠️ Goals detected but player names missing for fixture {fixture_id} - skipping fixture update")
                logger.info(f"🔄 Fixture will be retried in next monitoring cycle")
                
                # ✅ CRITICAL: Track this fixture as having incomplete goals
                fixtures_with_incomplete_goals.add(fixture_id)
                # ❌ NO fixture update - fixture remains "stale" for next cycle
            
        except Exception as e:
            logger.error(f"❌ Error fetching events for fixture {fixture_id}: {e}")
            continue
    
    # ✅ MODIFIED: Process completions - BUT SKIP if goals are incomplete
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"] 
        delta_result = completed_fixture["delta_result"]
        
        # ✅ CRITICAL CHECK: Don't complete if goals are incomplete
        if fixture_id in fixtures_with_incomplete_goals:
            logger.warning(f"⚠️ COMPLETION SKIPPED: Fixture {fixture_id} has incomplete goals")
            logger.info(f"🔄 Match will remain in fixtures_active until goals are processed")
            logger.info(f"📊 Current goals: {delta_result.get('current_goals', {})}")
            continue
        
        # ✅ SAFE TO COMPLETE: No incomplete goals detected
        try:
            # Get fixture context for completion flow naming
            fixture = store.fixtures_active.find_one({"_id": fixture_id})
            if fixture:
                home_team, away_team = store._extract_team_names(fixture)
                final_goals = delta_result.get("current_goals", {})
                home_score = final_goals.get("home", 0)
                away_score = final_goals.get("away", 0)
                completion_flow_name = f"🏁 COMPLETED: {home_team} {home_score}-{away_score} {away_team} (FT) [#{fixture_id}]"
            else:
                completion_flow_name = f"🏁 COMPLETED: Match #{fixture_id} (FT)"
            
            run_deployment(
                name="advance-flow/advance-flow",
                parameters={
                    "source_collection": "fixtures_active",
                    "destination_collection": "fixtures_completed",
                    "fixture_id": fixture_id
                },
                flow_run_name=completion_flow_name
            )
            
            completed_fixtures_count += 1
            logger.info(f"✅ Triggered completion flow: {completion_flow_name}")
            
        except Exception as e:
            logger.error(f"❌ Error triggering completion flow for fixture {fixture_id}: {e}")
            continue
    
    return {
        "status": "success",
        "active_fixtures": len(store.get_active_fixtures()),
        "goals_detected": delta_results["total_goals_detected"],
        "goal_flows_triggered": goal_flows_triggered,
        "completed_fixtures": completed_fixtures_count,
        "incomplete_goals_detected": len(fixtures_with_incomplete_goals),  # ✅ NEW METRIC
        "delta_results": delta_results
    }

@flow(
    name="monitor-flow",
    flow_run_name=generate_monitor_flow_name  # ✅ Safe - runs immediately
)
def monitor_flow():
    """Monitor flow - uses custom naming for all runs"""
    logger = get_run_logger()
    
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