# ✅ NEW: found_footy/flows/goal_flow.py
from prefect import flow
from typing import Optional, List

from found_footy.flows.flow_triggers import schedule_twitter_flow
# ✅ CLEAN: Direct import
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.utils.logging import get_logger, log_error_with_trace  # ✅ ADD

logger = get_logger(__name__)  # ✅ CHANGE

@flow(name="goal-flow")
def goal_flow(fixture_id: int, goal_events: Optional[List[dict]] = None):
    """Enhanced goal processing - handles both new goals and updates"""
    
    if not goal_events:
        logger.warning(f"⚠️ No goal events provided for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    logger.info(f"⚽ Processing {len(goal_events)} goal events for fixture {fixture_id}")
    
    store = FootyMongoStore()
    
    goals_created = []
    goals_updated = []
    goals_errors = []
    twitter_flows_scheduled = 0
    
    for goal_event in goal_events:
        try:
            # ✅ BOOLEAN HANDLING: True = new goal, False = updated/error
            is_new_goal = store.store_goal_pending(fixture_id, goal_event)
            
            minute = goal_event.get("time", {}).get("elapsed", 0)
            extra_time = goal_event.get("time", {}).get("extra") or 0
            
            # ✅ NEW: Generate goal_id using new format
            if extra_time > 0:
                goal_id = f"{fixture_id}_{minute}+{extra_time}"
            else:
                goal_id = f"{fixture_id}_{minute}"
            
            player_name = goal_event.get("player", {}).get("name", "Unknown")
            team_name = goal_event.get("team", {}).get("name", "Unknown")
            
            # ✅ CLEAN BOOLEAN LOGIC
            if is_new_goal:
                # ✅ NEW GOAL: Schedule Twitter flow
                schedule_result = schedule_twitter_flow(goal_id, delay_minutes=2)
                
                if schedule_result["status"] in ["scheduled", "immediate"]:
                    twitter_flows_scheduled += 1
                    goals_created.append(goal_id)
                    logger.info(f"✅ NEW goal: {team_name} - {player_name} ({minute}')")
                    logger.info(f"⏰ Scheduled Twitter search")
                
            else:
                # ✅ EXISTING GOAL: Just track the update, no new flow
                goals_updated.append(goal_id)
                logger.info(f"🔄 UPDATED goal: {team_name} - {player_name} ({minute}'+{extra_time})")
                logger.info(f"ℹ️ Player/details updated, no new Twitter search needed")
                
        except Exception as e:
            logger.error(f"❌ Failed to process goal: {e}")
            goals_errors.append(f"exception_{goal_event.get('time', {}).get('elapsed', 0)}")
    
    logger.info(f"📊 Summary: {len(goals_created)} new, {len(goals_updated)} updated, {len(goals_errors)} errors")
    logger.info(f"⏰ Scheduled {twitter_flows_scheduled} Twitter search flows")
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "goals_created": len(goals_created),
        "goals_updated": len(goals_updated),
        "goals_errors": len(goals_errors),
        "twitter_flows_scheduled": twitter_flows_scheduled,
        "new_goals": goals_created,
        "updated_goals": goals_updated,
        "error_goals": goals_errors,
        "pipeline": "goal → [5min delay] → twitter_search → download → s3"
    }