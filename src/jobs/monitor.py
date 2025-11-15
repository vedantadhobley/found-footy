"""Monitor fixtures job - detects goal changes and triggers goal pipelines

Migrated from found_footy/flows/monitor_flow.py

This is the ORCHESTRATOR pattern - directly launches Dagster job runs when goals are detected.
Based on Prefect's monitor_flow.py which uses run_deployment() for direct triggering.

Schedule: Every 5 minutes
- Only queries API when fixtures_active is not empty ✅
- Detects goal changes with batch API calls ✅
- DIRECTLY triggers goal_pipeline via execute_in_process() ✅
- Updates fixtures in MongoDB after validation ✅
- Advances completed fixtures to completed collection ✅
"""

import logging
from typing import Dict, Any
from datetime import datetime, timezone

from dagster import job, op, OpExecutionContext
from src.data.mongo_store import FootyMongoStore
from src.api.mongo_api import fixtures_events
from src.utils import fixture_logic

logger = logging.getLogger(__name__)


@op(
    name="monitor_fixtures_op",
    description="Monitor active fixtures and dynamically trigger goal_pipeline for detected goals"
)
def monitor_fixtures_op(context: OpExecutionContext) -> Dict[str, Any]:
    """
    Monitor all active fixtures for goal changes and trigger goal_pipeline dynamically.
    
    This op is the ORCHESTRATOR - it launches goal_pipeline jobs when goals are detected.
    Equivalent to Prefect's monitor_flow.py which uses run_deployment() for direct triggering.
    """
    store = FootyMongoStore()
    
    # Check if there are active fixtures
    active_count = store.fixtures_active.count_documents({})
    if active_count == 0:
        context.log.info("⏸️ No active fixtures - skipping monitoring")
        return {
            "status": "no_work_skipped",
            "reason": "no_active_fixtures",
            "active_fixtures": 0,
            "goals_detected": 0
        }
    
    context.log.info(f"🔍 Monitoring {active_count} active fixtures")
    
    # Log fixture IDs being monitored
    active_fixtures = list(store.fixtures_active.find({}, {"_id": 1}))
    fixture_ids = [f["_id"] for f in active_fixtures]
    context.log.info(f"   Fixture IDs: {fixture_ids}")
    
    # Use batch delta detection (same as Prefect's fixtures_delta_task)
    delta_results = fixture_logic.detect_fixture_deltas(store)
    
    context.log.info(f"📊 Delta detection status: {delta_results['status']}")
    context.log.info(f"   Fixtures with changes: {len(delta_results.get('fixtures_with_changes', []))}")
    context.log.info(f"   Fixtures completed: {len(delta_results.get('fixtures_completed', []))}")
    context.log.info(f"   Total goals detected: {delta_results.get('total_goals_detected', 0)}")
    
    if delta_results["status"] != "success":
        context.log.error(f"❌ Delta detection failed: {delta_results.get('error', 'Unknown error')}")
        return {
            "status": "error",
            "delta_results": delta_results
        }
    
    goal_flows_triggered = 0
    completed_fixtures_count = 0
    fixtures_with_incomplete_goals = {}
    
    # Process fixtures with goal changes
    for fixture_change in delta_results["fixtures_with_changes"]:
        fixture_id = fixture_change["fixture_id"]
        delta_result = fixture_change["delta_result"]
        
        if not delta_result.get("goals_changed", False):
            context.log.info(f"⚪ Fixture {fixture_id} - no goal changes")
            continue
        
        total_increase = delta_result.get("total_goal_increase", 0)
        current_goals = delta_result.get("current_goals", {})
        home_score = current_goals.get("home", 0)
        away_score = current_goals.get("away", 0)
        expected_total_goals = home_score + away_score
        
        context.log.info(f"🚨 GOAL DELTA: Fixture {fixture_id} - +{total_increase} goals (now {home_score}-{away_score})")
        
        # Fetch actual events from API (same as Prefect monitor_flow.py)
        try:
            events_list = fixtures_events(fixture_id)
            complete_goal_events = []
            incomplete_goal_events = []
            
            # Separate complete vs incomplete goals
            for event in events_list:
                if event.get("type") == "Goal" and event.get("detail") != "Missed Penalty":
                    if event.get("player", {}).get("name"):
                        complete_goal_events.append(event)
                    else:
                        incomplete_goal_events.append(event)
            
            actual_complete_count = len(complete_goal_events)
            actual_incomplete_count = len(incomplete_goal_events)
            total_event_count = actual_complete_count + actual_incomplete_count
            
            context.log.info(f"📊 Events: {actual_complete_count} complete, {actual_incomplete_count} incomplete")
            
            # Process available complete goals - TRIGGER GOAL PIPELINE
            if actual_complete_count > 0:
                context.log.info(f"✅ Processing {actual_complete_count} complete goals")
                
                # ⚡ DAGSTER DIRECT JOB TRIGGERING
                # Import here to avoid circular dependency
                from src.jobs.goal_pipeline import goal_pipeline_job
                
                # Equivalent to Prefect's: run_deployment("goal-flow/goal-flow", parameters={...})
                run_config = {
                    "ops": {
                        "process_goal_events_op": {
                            "config": {
                                "fixture_id": fixture_id,
                                "goal_events": complete_goal_events
                            }
                        }
                    }
                }
                
                # Launch goal_pipeline job dynamically
                # Using execute_in_process for synchronous execution (goals processed immediately)
                result = goal_pipeline_job.execute_in_process(
                    run_config=run_config,
                    instance=context.instance,
                    tags={
                        "fixture_id": str(fixture_id),
                        "goal_count": str(actual_complete_count),
                        "triggered_by": "monitor_fixtures"
                    }
                )
                
                goal_flows_triggered += 1
                if result.success:
                    context.log.info(f"⚡ goal_pipeline SUCCESS: fixture={fixture_id}, goals={actual_complete_count}")
                else:
                    context.log.error(f"❌ goal_pipeline FAILED: fixture={fixture_id}")
            else:
                context.log.warning(f"⚠️ NO COMPLETE GOALS: All {total_event_count} goals missing player data")
            
            # Update fixture if all goals are complete
            if total_event_count == expected_total_goals and actual_incomplete_count == 0:
                context.log.info(f"✅ ALL GOALS COMPLETE: Updating fixture {fixture_id}")
                
                clean_update = {
                    "goals": current_goals,
                    "score": delta_result.get("current_score", {})
                }
                store.fixtures_active.update_one(
                    {"_id": fixture_id},
                    {"$set": clean_update}
                )
                context.log.info(f"✅ Updated fixture {fixture_id}: {home_score}-{away_score}")
            else:
                # Keep fixture stale for retry
                if actual_incomplete_count > 0:
                    context.log.warning(f"⚠️ FIXTURE KEPT STALE: {actual_incomplete_count} incomplete goals")
                elif total_event_count < expected_total_goals:
                    context.log.warning(f"⚠️ FIXTURE KEPT STALE: Only {total_event_count}/{expected_total_goals} events")
                
                context.log.info(f"🔄 Fixture {fixture_id} will retry in next cycle")
                
                fixtures_with_incomplete_goals[fixture_id] = {
                    "expected_goals": expected_total_goals,
                    "complete_goals": actual_complete_count,
                    "incomplete_goals": actual_incomplete_count,
                    "missing_events": expected_total_goals - total_event_count
                }
        
        except Exception as e:
            context.log.error(f"❌ Error processing fixture {fixture_id}: {e}")
            continue
    
    # Process completed fixtures (same as Prefect monitor_flow.py)
    for completed_fixture in delta_results["fixtures_completed"]:
        fixture_id = completed_fixture["fixture_id"]
        delta_result = completed_fixture["delta_result"]
        
        current_goals = delta_result.get("current_goals", {})
        expected_total = current_goals.get("home", 0) + current_goals.get("away", 0)
        
        if expected_total > 0:
            try:
                # Validate with fresh API data
                context.log.info(f"🔍 Validating completion: fixture {fixture_id}, expected {expected_total} goals")
                
                fresh_events = fixtures_events(fixture_id)
                fresh_goal_events = [
                    e for e in fresh_events 
                    if e.get("type") == "Goal" and e.get("detail") != "Missed Penalty"
                ]
                fresh_api_count = len(fresh_goal_events)
                
                context.log.info(f"📊 Validation: fixture={expected_total}, API={fresh_api_count}")
                
                if fresh_api_count != expected_total:
                    context.log.warning(f"⚠️ Mismatch: fixture={expected_total}, API={fresh_api_count}")
                    continue
                
                # Check if all events have complete data
                complete_events = [e for e in fresh_goal_events if e.get("player", {}).get("name")]
                incomplete_events = len(fresh_goal_events) - len(complete_events)
                
                if incomplete_events > 0:
                    context.log.warning(f"⚠️ {incomplete_events}/{fresh_api_count} events incomplete")
                    continue
                
                context.log.info(f"✅ Validation passed: {fresh_api_count} goals, all complete")
                
            except Exception as e:
                context.log.error(f"❌ Validation failed: {e}")
                continue
        
        # Complete the fixture
        try:
            context.log.info(f"🏁 Completing fixture {fixture_id}")
            
            fixture_doc = store.fixtures_active.find_one({"_id": fixture_id})
            if fixture_doc:
                # Update with final API data
                fixture_doc.update(delta_result.get("api_data", {}))
                fixture_doc["completed_at"] = datetime.now(timezone.utc)
                
                store.fixtures_completed.insert_one(fixture_doc)
                store.fixtures_active.delete_one({"_id": fixture_id})
                
                completed_fixtures_count += 1
                context.log.info(f"✅ Fixture {fixture_id} moved to completed")
            else:
                context.log.error(f"❌ Fixture {fixture_id} not found in active")
        
        except Exception as e:
            context.log.error(f"❌ Error completing fixture {fixture_id}: {e}")
            continue
    
    return {
        "status": "success",
        "active_fixtures": active_count,
        "goals_detected": delta_results["total_goals_detected"],
        "goal_flows_triggered": goal_flows_triggered,
        "completed_fixtures": completed_fixtures_count,
        "fixtures_with_incomplete_goals": len(fixtures_with_incomplete_goals),
        "timestamp": datetime.now(timezone.utc).isoformat()
    }


@job(
    name="monitor_fixtures",
    description="Monitor active fixtures for goal changes and dynamically trigger goal_pipeline"
)
def monitor_fixtures_job():
    """
    Monitor workflow - scans fixtures, detects goal changes, and triggers goal_pipeline dynamically.
    
    This is the ORCHESTRATOR pattern - directly launches Dagster runs when goals are detected.
    Equivalent to Prefect's monitor_flow which uses run_deployment() for direct triggering.
    """
    monitor_fixtures_op()
