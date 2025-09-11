# ✅ NEW: found_footy/flows/goal_flow.py
from prefect import flow, get_run_logger
from prefect.deployments import run_deployment
from typing import Optional, List

from found_footy.flows.shared_tasks import store
from found_footy.flows.flow_naming import get_twitter_flow_name  # ✅ UPDATED

@flow(
    name="goal-flow"
    # ❌ NO flow_run_name here - will be set by triggering code
)
def goal_flow(fixture_id: int, goal_events: Optional[List[dict]] = None):
    """Dedicated goal processing flow - triggers Twitter directly"""
    logger = get_run_logger()
    
    if not goal_events:
        logger.warning(f"⚠️ No goal events provided for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    logger.info(f"⚽ Processing {len(goal_events)} goal events for fixture {fixture_id}")
    
    goals_processed = []
    twitter_flows_triggered = 0
    
    for goal_event in goal_events:
        try:
            # Store the goal first
            if store.store_goal_pending(fixture_id, goal_event):  # ✅ UPDATED METHOD
                minute = goal_event.get("time", {}).get("elapsed", 0)
                player_id = goal_event.get("player", {}).get("id", 0)
                goal_id = f"{fixture_id}_{minute}_{player_id}"
                
                player_name = goal_event.get("player", {}).get("name", "Unknown")
                team_name = goal_event.get("team", {}).get("name", "Unknown")
                
                # Trigger Twitter flow with rich naming using new method
                twitter_flow_name = get_twitter_flow_name(goal_id)  # ✅ UPDATED
                
                run_deployment(
                    name="twitter-flow/twitter-flow",
                    parameters={"goal_id": goal_id},
                    flow_run_name=twitter_flow_name
                )
                
                twitter_flows_triggered += 1
                goals_processed.append(goal_id)
                
                logger.info(f"✅ Stored goal: {team_name} - {player_name} ({minute}')")
                logger.info(f"🐦 Triggered Twitter: {twitter_flow_name}")
                
        except Exception as e:
            logger.error(f"❌ Failed to process goal: {e}")
    
    logger.info(f"📡 Triggered {twitter_flows_triggered} Twitter flows directly")
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "goals_processed": len(goals_processed),
        "twitter_flows_triggered": twitter_flows_triggered,
        "valid_goals": goals_processed
    }