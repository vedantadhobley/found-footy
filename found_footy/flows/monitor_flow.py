# ✅ NEW: found_footy/flows/monitor_flow.py
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from typing import List
from datetime import datetime, timezone  # ✅ ADD: Missing imports

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
    
    # Process completions - ✅ FIXED validation using fresh API data
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"] 
        delta_result = completed_fixture["delta_result"]
        
        current_goals = delta_result.get("current_goals", {})
        expected_total = current_goals.get("home", 0) + current_goals.get("away", 0)
        
        if expected_total > 0:
            try:
                # ✅ FIX: Get FRESH API events for validation
                logger.info(f"🔍 Validating completion for fixture {fixture_id}: expected {expected_total} goals")
                
                # ENHANCEMENT: Add error handling for API failures
                try:
                    fresh_events = fixtures_events(fixture_id)  # Fresh API call
                except Exception as api_error:
                    logger.error(f"❌ API call failed for fixture {fixture_id}: {api_error}")
                    logger.info(f"🔄 Will retry in next monitoring cycle")
                    continue  # Skip this fixture, don't fail entire monitor task
                
                fresh_goal_events = [e for e in fresh_events if e.get("type") == "Goal" and e.get("detail") != "Missed Penalty"]
                fresh_api_count = len(fresh_goal_events)
                
                logger.info(f"📊 VALIDATION DATA:")
                logger.info(f"   Fixture shows: {expected_total} goals")
                logger.info(f"   Fresh API events: {fresh_api_count} goal events")
                
                # ✅ CORRECT: Compare fixture goals vs fresh API events (not stored goals)
                if fresh_api_count != expected_total:
                    logger.warning(f"⚠️ COMPLETION SKIPPED: Fixture {fixture_id} shows {expected_total} goals but API events show {fresh_api_count}")
                    logger.info(f"💡 This suggests API data inconsistency - will retry in next cycle")
                    continue
                
                # ✅ ADDITIONAL: Check if events have complete data
                complete_events = [e for e in fresh_goal_events if e.get("player", {}).get("name")]
                incomplete_events = len(fresh_goal_events) - len(complete_events)
                
                if incomplete_events > 0:
                    logger.warning(f"⚠️ COMPLETION SKIPPED: {incomplete_events}/{fresh_api_count} events missing player data")
                    continue
                
                logger.info(f"✅ VALIDATION PASSED: {fresh_api_count} goals match {expected_total} expected, all have complete data")
                
            except Exception as api_error:
                logger.error(f"❌ API validation failed for fixture {fixture_id}: {api_error}")
                logger.info(f"🔄 Will retry completion in next cycle")
                continue
        
        # ✅ PROCEED: Complete the fixture
        try:
            logger.info(f"🏁 COMPLETING FIXTURE: {fixture_id}")
            
            # Move from active to completed
            fixture_doc = store.fixtures_active.find_one({"_id": fixture_id})
            if fixture_doc:
                # Update with final API data
                fixture_doc.update(delta_result.get("api_data", {}))
                fixture_doc["completed_at"] = datetime.now(timezone.utc)
                
                # Store in completed collection
                store.fixtures_completed.insert_one(fixture_doc)
                
                # Remove from active collection
                store.fixtures_active.delete_one({"_id": fixture_id})
                
                completed_fixtures_count += 1
                logger.info(f"✅ Fixture {fixture_id} moved to completed collection")
            else:
                logger.error(f"❌ Fixture {fixture_id} not found in active collection")
                
        except Exception as e:
            logger.error(f"❌ Error completing fixture {fixture_id}: {e}")
            continue
    
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