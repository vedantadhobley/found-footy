# ✅ NEW: found_footy/flows/goal_flow.py
from prefect import flow, get_run_logger
from typing import Optional, List

from found_footy.flows.shared_tasks import store
from found_footy.flows.flow_triggers import schedule_twitter_flow  # ✅ NEW IMPORT

@flow(
    name="goal-flow"
    # ❌ NO flow_run_name here - will be set by triggering code
)
def goal_flow(fixture_id: int, goal_events: Optional[List[dict]] = None):
    """Dedicated goal processing flow - triggers Twitter → Download chain with 5min delay"""
    logger = get_run_logger()
    
    if not goal_events:
        logger.warning(f"⚠️ No goal events provided for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    # ✅ Filter only actual goals
    actual_goals = [event for event in goal_events if event.get("type") == "Goal"]
    
    if not actual_goals:
        logger.info(f"⚽ No actual goals found in {len(goal_events)} events for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    logger.info(f"⚽ Processing {len(actual_goals)} goal events (filtered from {len(goal_events)} total events) for fixture {fixture_id}")
    
    # ✅ NEW: Get existing goal IDs to detect new vs updated goals
    existing_goal_ids = store.get_existing_goal_ids(fixture_id)
    logger.info(f"📋 Found {len(existing_goal_ids)} existing goals for fixture {fixture_id}")
    
    new_goals = []
    updated_goals = []
    twitter_flows_scheduled = 0
    
    for goal_event in actual_goals:
        try:
            # ✅ Generate goal ID using your format
            time_data = goal_event.get("time", {})
            elapsed = time_data.get("elapsed", 0)
            extra = time_data.get("extra")
            
            if extra is not None:
                goal_id = f"{fixture_id}_{elapsed}_{extra}"
            else:
                goal_id = f"{fixture_id}_{elapsed}"
            
            # ✅ Check if this is a new goal or update to existing
            is_new_goal = goal_id not in existing_goal_ids
            
            # ✅ Store/update the goal (always store latest API data)
            if store.store_goal_pending(fixture_id, goal_event):
                player_name = goal_event.get("player", {}).get("name", "Unknown")
                team_name = goal_event.get("team", {}).get("name", "Unknown")
                
                if is_new_goal:
                    # ✅ NEW GOAL: Trigger Twitter flow
                    logger.info(f"🆕 NEW GOAL: {team_name} - {player_name} ({elapsed}')")
                    
                    schedule_result = schedule_twitter_flow(goal_id, delay_minutes=2)
                    
                    if schedule_result["status"] == "scheduled":
                        twitter_flows_scheduled += 1
                        new_goals.append(goal_id)
                        logger.info(f"⏰ Scheduled Twitter search in 2 minutes: {schedule_result['scheduled_time']}")
                        
                    elif schedule_result["status"] == "immediate":
                        twitter_flows_scheduled += 1
                        new_goals.append(goal_id)
                        logger.info(f"🔍 Triggered immediate Twitter search")
                        
                    else:
                        logger.error(f"❌ Failed to schedule Twitter flow: {schedule_result.get('error')}")
                        
                else:
                    # ✅ EXISTING GOAL: Update only, no Twitter flow
                    updated_goals.append(goal_id)
                    logger.info(f"🔄 UPDATED: {team_name} - {player_name} ({elapsed}') - Data refreshed, no new Twitter flow")
                
        except Exception as e:
            logger.error(f"❌ Failed to process goal: {e}")
    
    logger.info(f"📊 GOAL PROCESSING SUMMARY:")
    logger.info(f"   🆕 New goals: {len(new_goals)} (triggered {twitter_flows_scheduled} Twitter flows)")
    logger.info(f"   🔄 Updated goals: {len(updated_goals)} (data refreshed only)")
    logger.info("📥 Download flows will be triggered after video discovery")
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "new_goals": len(new_goals),
        "updated_goals": len(updated_goals),
        "twitter_flows_scheduled": twitter_flows_scheduled,
        "new_goal_ids": new_goals,
        "updated_goal_ids": updated_goals,
        "delay_minutes": 2,
        "pipeline": "new_goal → [2min delay] → twitter_search → download → s3"
    }