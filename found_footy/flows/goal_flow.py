# ✅ NEW: found_footy/flows/goal_flow.py
from prefect import flow, get_run_logger
from typing import Optional, List

from found_footy.flows.shared_tasks import store
from found_footy.flows.flow_triggers import schedule_twitter_flow

@flow(name="goal-flow")
def goal_flow(fixture_id: int, goal_events: Optional[List[dict]] = None):
    """Goal processing flow with single collection and new implementation"""
    logger = get_run_logger()
    
    if not goal_events:
        logger.warning(f"⚠️ No goal events provided for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    actual_goals = [event for event in goal_events if event.get("type") == "Goal"]
    
    if not actual_goals:
        logger.info(f"⚽ No actual goals found in {len(goal_events)} events for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    logger.info(f"⚽ Processing {len(actual_goals)} goal events for fixture {fixture_id}")
    
    # Get existing goal IDs from single collection
    existing_goal_ids = store.get_existing_goal_ids(fixture_id)
    logger.info(f"📋 Found {len(existing_goal_ids)} existing goals for fixture {fixture_id}")
    
    new_goals = []
    updated_goals = []
    twitter_flows_scheduled = 0
    
    for goal_event in actual_goals:
        try:
            # Generate goal ID
            time_data = goal_event.get("time", {})
            elapsed = time_data.get("elapsed", 0)
            extra = time_data.get("extra")
            
            if extra is not None and extra > 0:
                goal_id = f"{fixture_id}_{elapsed}+{extra}"
            else:
                goal_id = f"{fixture_id}_{elapsed}"
            
            is_new_goal = goal_id not in existing_goal_ids
            
            # ✅ NEW IMPLEMENTATION: Use store_goal directly with processing_status
            if is_new_goal:
                # NEW goal - store and trigger Twitter flow
                if store.store_goal(fixture_id, goal_event, processing_status="discovered"):
                    player_name = goal_event.get("player", {}).get("name", "Unknown")
                    team_name = goal_event.get("team", {}).get("name", "Unknown")
                    display_minute = f"{elapsed}+{extra}" if extra else str(elapsed)
                    
                    logger.info(f"🆕 NEW GOAL: {team_name} - {player_name} ({display_minute}') [{goal_id}]")
                    
                    # Schedule Twitter flow with 2-minute delay
                    try:
                        schedule_result = schedule_twitter_flow(goal_id, delay_minutes=2)
                        
                        if schedule_result["status"] in ["scheduled", "immediate"]:
                            twitter_flows_scheduled += 1
                            new_goals.append(goal_id)
                            logger.info(f"📅 Scheduled Twitter flow for {goal_id}")
                        else:
                            logger.error(f"❌ Failed to schedule Twitter flow: {schedule_result}")
                            
                    except Exception as e:
                        logger.error(f"❌ Error scheduling Twitter flow for {goal_id}: {e}")
                        new_goals.append(goal_id)
                        
            else:
                # EXISTING goal - just update data
                store.store_goal(fixture_id, goal_event, processing_status="discovered")
                logger.info(f"🔄 UPDATED GOAL: {goal_id} (data refreshed)")
                updated_goals.append(goal_id)
                
        except Exception as e:
            logger.error(f"❌ Error processing goal event: {e}")
            continue
    
    logger.info(f"✅ Goal processing complete for fixture {fixture_id}:")
    logger.info(f"   🆕 New goals: {len(new_goals)} (triggered {twitter_flows_scheduled} Twitter flows)")
    logger.info(f"   🔄 Updated goals: {len(updated_goals)}")
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "new_goals": len(new_goals),
        "updated_goals": len(updated_goals),
        "twitter_flows_scheduled": twitter_flows_scheduled,
        "new_goal_ids": new_goals,
        "updated_goal_ids": updated_goals,
        "processing_method": "single_collection_unified"
    }