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
    
    logger.info(f"⚽ Processing {len(goal_events)} goal events for fixture {fixture_id}")
    
    goals_processed = []
    twitter_flows_scheduled = 0
    
    for goal_event in goal_events:
        try:
            # Store the goal first
            if store.store_goal_pending(fixture_id, goal_event):
                minute = goal_event.get("time", {}).get("elapsed", 0)
                player_id = goal_event.get("player", {}).get("id", 0)
                goal_id = f"{fixture_id}_{minute}_{player_id}"
                
                player_name = goal_event.get("player", {}).get("name", "Unknown")
                team_name = goal_event.get("team", {}).get("name", "Unknown")
                
                # ✅ NEW: Schedule Twitter search flow with 5-minute delay
                schedule_result = schedule_twitter_flow(goal_id, delay_minutes=2)
                
                if schedule_result["status"] == "scheduled":
                    twitter_flows_scheduled += 1
                    goals_processed.append(goal_id)
                    
                    logger.info(f"✅ Stored goal: {team_name} - {player_name} ({minute}')")
                    logger.info(f"⏰ Scheduled Twitter search in 5 minutes: {schedule_result['scheduled_time']}")
                    
                elif schedule_result["status"] == "immediate":
                    twitter_flows_scheduled += 1
                    goals_processed.append(goal_id)
                    
                    logger.info(f"✅ Stored goal: {team_name} - {player_name} ({minute}')")
                    logger.info(f"🔍 Triggered immediate Twitter search (no delay)")
                    
                else:
                    logger.error(f"❌ Failed to schedule Twitter flow: {schedule_result.get('error')}")
                
        except Exception as e:
            logger.error(f"❌ Failed to process goal: {e}")
    
    logger.info(f"⏰ Scheduled {twitter_flows_scheduled} Twitter search flows (5-minute delay)")
    logger.info("📥 Download flows will be triggered after video discovery")
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "goals_processed": len(goals_processed),
        "twitter_flows_scheduled": twitter_flows_scheduled,
        "valid_goals": goals_processed,
        "delay_minutes": 5,
        "pipeline": "goal → [5min delay] → twitter_search → download → s3"
    }