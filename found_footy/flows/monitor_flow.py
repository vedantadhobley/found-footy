# ✅ NEW: found_footy/flows/monitor_flow.py
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from typing import List

from found_footy.flows.shared_tasks import fixtures_delta_task, store
from found_footy.api.mongo_api import fixtures_events
from found_footy.flows.flow_naming import generate_monitor_flow_name

@task(name="fixtures-monitor-task")
def fixtures_monitor_task():
    """Monitor active fixtures - trigger goal flows for AVAILABLE goals, keep incomplete fixtures stale"""
    logger = get_run_logger()
    
    delta_results = fixtures_delta_task()
    
    if delta_results["status"] != "success":
        logger.error("❌ Delta detection failed")
        return {"status": "error", "delta_results": delta_results}
    
    goal_flows_triggered = 0
    completed_fixtures_count = 0
    
    # Track fixtures with incomplete goals for reporting
    fixtures_with_incomplete_goals = {}
    
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
        expected_total_goals = home_score + away_score
        
        logger.info(f"🚨 GOAL DELTA DETECTED: Fixture {fixture_id} - +{total_increase} goals (now {home_score}-{away_score})")
        
        # Fetch actual EVENTS data from API
        try:
            events_list = fixtures_events(fixture_id)
            complete_goal_events = []
            incomplete_goal_events = []
            
            # ✅ Separate complete vs incomplete goals
            for event in events_list:
                if event.get("type") == "Goal" and event.get("detail") != "Missed Penalty":
                    if event.get("player", {}).get("name"):  # Has player name
                        complete_goal_events.append(event)
                    else:  # Missing player name
                        incomplete_goal_events.append(event)
            
            actual_complete_count = len(complete_goal_events)
            actual_incomplete_count = len(incomplete_goal_events)
            total_event_count = actual_complete_count + actual_incomplete_count
            
            logger.info(f"📊 GOAL EVENT ANALYSIS:")
            logger.info(f"   Expected total goals: {expected_total_goals}")
            logger.info(f"   Complete events: {actual_complete_count}")
            logger.info(f"   Incomplete events: {actual_incomplete_count}")
            logger.info(f"   Total events found: {total_event_count}")
            
            # ✅ NEW LOGIC: Process available goals even if some are incomplete
            if actual_complete_count > 0:
                # ✅ PROCESS AVAILABLE COMPLETE GOALS
                logger.info(f"✅ PROCESSING {actual_complete_count} COMPLETE GOALS")
                
                # Get fixture context for flow naming
                fixture = store.fixtures_active.find_one({"_id": fixture_id})
                if fixture:
                    home_team, away_team = store._extract_team_names(fixture)
                    flow_run_name = f"⚽ GOALS: {home_team} {home_score}-{away_score} {away_team} - {actual_complete_count}/{expected_total_goals} events [#{fixture_id}]"
                else:
                    flow_run_name = f"⚽ GOALS: Match #{fixture_id} - {home_score}-{away_score} - {actual_complete_count}/{expected_total_goals} events"

                run_deployment(
                    name="goal-flow/goal-flow",
                    parameters={
                        "fixture_id": fixture_id,
                        "goal_events": complete_goal_events  # Only pass complete goals
                    },
                    flow_run_name=flow_run_name
                )
                
                goal_flows_triggered += 1
                logger.info(f"✅ Triggered goal flow: {flow_run_name}")
                
            else:
                logger.warning(f"⚠️ NO COMPLETE GOALS: All {total_event_count} goals missing player data")
            
            # ✅ FIXTURE UPDATE LOGIC: Only if ALL goals are complete
            if total_event_count == expected_total_goals and actual_incomplete_count == 0:
                # ✅ COMPLETE MATCH: All goals have complete data - safe to update fixture
                logger.info(f"✅ ALL GOALS COMPLETE: Updating fixture {fixture_id}")
                
                current_fixture = store.fixtures_active.find_one({"_id": fixture_id})
                if current_fixture:
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
                # ❌ INCOMPLETE: Keep fixture stale for retry
                if actual_incomplete_count > 0:
                    logger.warning(f"⚠️ FIXTURE KEPT STALE: {actual_incomplete_count} goals missing player data")
                elif total_event_count < expected_total_goals:
                    logger.warning(f"⚠️ FIXTURE KEPT STALE: Only {total_event_count}/{expected_total_goals} events found")
                
                logger.info(f"🔄 Fixture {fixture_id} will be retried in next monitoring cycle")
                
                # Track incomplete fixtures for reporting
                fixtures_with_incomplete_goals[fixture_id] = {
                    "expected_goals": expected_total_goals,
                    "complete_goals": actual_complete_count,
                    "incomplete_goals": actual_incomplete_count,
                    "missing_events": expected_total_goals - total_event_count
                }
                
                # ❌ NO fixture update - fixture remains "stale" for next cycle
            
        except Exception as e:
            logger.error(f"❌ Error fetching events for fixture {fixture_id}: {e}")
            continue
    
    # Process completions - enhanced validation
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"] 
        delta_result = completed_fixture["delta_result"]
        
        # ✅ CRITICAL CHECK: Don't complete if goals are incomplete
        if fixture_id in fixtures_with_incomplete_goals:
            incomplete_info = fixtures_with_incomplete_goals[fixture_id]
            logger.warning(f"⚠️ COMPLETION SKIPPED: Fixture {fixture_id} has incomplete goals")
            logger.info(f"📊 Incomplete details: {incomplete_info}")
            continue
        
        # ✅ FIX: Updated validation for single collection
        current_goals = delta_result.get("current_goals", {})
        expected_total = current_goals.get("home", 0) + current_goals.get("away", 0)
        
        if expected_total > 0:
            # ✅ Use single collection validation method
            validation = store.validate_goal_count(fixture_id, expected_total)
            if not validation["is_valid"]:
                logger.warning(f"⚠️ COMPLETION SKIPPED: Fixture {fixture_id} has {validation['stored_count']} completed goals but fixture shows {validation['expected_count']}")
                continue
        
        # ✅ SAFE TO COMPLETE: All goals are validated and complete
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
            logger.error(f"❌ Error completing fixture {fixture_id}: {e}")
    
    return {
        "status": "success",
        "goals_detected": delta_results["total_goals_detected"],
        "goal_flows_triggered": goal_flows_triggered,
        "completed_fixtures": completed_fixtures_count,
        "fixtures_with_incomplete_goals": len(fixtures_with_incomplete_goals),
        "incomplete_goal_details": fixtures_with_incomplete_goals,
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