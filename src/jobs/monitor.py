"""Monitor fixtures job - detects goal changes and triggers processing

Migrated from found_footy/flows/monitor_flow.py

Schedule: Every 5 minutes (reduced from 3min to save ~33% API costs)
- Only queries API when fixtures_active is not empty ✅
- Detects goal changes and updates fixtures in MongoDB ✅
- Sensor picks up unprocessed goals from fixtures collection ✅
- No delay logic - sensor triggers immediately, retry logic added later ✅
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
    description="Scan active fixtures, detect goal changes, and process them"
)
def monitor_fixtures_op(context: OpExecutionContext) -> Dict[str, Any]:
    """
    Monitor all active fixtures for goal changes.
    This op does the full monitoring cycle in one operation.
    """
    store = FootyMongoStore()
    
    # Check if we have work to do
    if store.check_collections_empty(["fixtures_active"]):
        context.log.info("⏸️ No active fixtures - skipping")
        return {"status": "no_work", "fixtures_processed": 0}
    
    context.log.info("🔍 Scanning active fixtures for changes...")
    
    # Use existing fixture_logic
    delta_results = fixture_logic.detect_fixture_deltas(store)
    
    if delta_results["status"] != "success":
        context.log.error("❌ Delta detection failed")
        return {"status": "error", "fixtures_processed": 0}
    
    fixtures_with_changes = delta_results.get("fixtures_with_changes", [])
    context.log.info(f"🚨 Found {len(fixtures_with_changes)} fixtures with changes")
    
    goals_detected = 0
    fixtures_updated = 0
    
    # Process each fixture with changes
    for fixture_change in fixtures_with_changes:
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
        
        # Fetch actual events from API
        try:
            events_list = fixtures_events(fixture_id)
            complete_goal_events = []
            incomplete_goal_events = []
            
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
            
            if actual_complete_count > 0:
                context.log.info(f"✅ Found {actual_complete_count} complete goals for fixture {fixture_id}")
                goals_detected += actual_complete_count
                
                # Goals are already in fixture document, sensor will find them
                context.log.info(f"📝 {actual_complete_count} goals available for sensor to process")
            
            # Update fixture if all goals are complete
            if total_event_count == expected_total_goals and actual_incomplete_count == 0:
                context.log.info(f"✅ All goals complete - updating fixture {fixture_id}")
                current_fixture = store.fixtures_active.find_one({"_id": fixture_id})
                if current_fixture:
                    clean_update = {
                        "goals": complete_goal_events,  # Store complete goal events for sensor
                        "score": delta_result.get("current_score", {})
                    }
                    store.fixtures_active.update_one(
                        {"_id": fixture_id}, 
                        {"$set": clean_update}
                    )
                    fixtures_updated += 1
            else:
                context.log.warning(f"⚠️ Fixture {fixture_id} kept stale - will retry next cycle")
                
        except Exception as e:
            context.log.error(f"❌ Error processing fixture {fixture_id}: {e}")
    
    return {
        "status": "success",
        "fixtures_with_changes": len(fixtures_with_changes),
        "goals_detected": goals_detected,
        "fixtures_updated": fixtures_updated
    }


@job(
    name="monitor_fixtures",
    description="Monitor active fixtures for goal changes and detect what needs processing"
)
def monitor_fixtures_job():
    """
    Monitor workflow - scans fixtures and detects goal changes.
    Returns data that can be used by sensors to trigger downstream jobs.
    """
    monitor_fixtures_op()
