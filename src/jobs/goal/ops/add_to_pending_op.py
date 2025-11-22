"""Process new goals and add them to pending collection"""
from datetime import datetime, timezone
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="add_to_pending",
    description="Add new goals to goals_pending collection",
    tags={"kind": "database", "collection": "goals_pending"}
)
def add_to_pending_op(
    context: OpExecutionContext,
    goal_status: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Add new goals to the goals_pending collection.
    These goals will be validated in the next monitor cycle.
    
    Args:
        goal_status: Dict containing new_goals list
        
    Returns:
        Dict with count of goals added to pending
    """
    new_goals = goal_status.get("new_goals", [])
    
    if not new_goals:
        context.log.info("✅ No new goals to add to pending")
        return {"status": "success", "added_count": 0}
    
    store = FootyMongoStore()
    fixture_id = goal_status["fixture_id"]
    
    added_count = 0
    
    for goal_data in new_goals:
        goal_id = goal_data["goal_id"]
        event = goal_data["event"]
        
        try:
            # Create pending goal document
            pending_doc = {
                "_id": goal_id,
                "fixture_id": fixture_id,
                "event_data": event,
                "created_at": datetime.now(timezone.utc).isoformat(),
                "validation_attempts": 0
            }
            
            store.goals_pending.insert_one(pending_doc)
            
            player = event.get("player", {}).get("name", "Unknown")
            team = event.get("team", {}).get("name", "Unknown")
            minute = event.get("time", {}).get("elapsed", "?")
            extra = event.get("time", {}).get("extra")
            minute_str = f"{minute}+{extra}" if extra else str(minute)
            
            context.log.info(f"✅ Added to pending: {goal_id} - {player} ({team}) {minute_str}'")
            added_count += 1
            
        except Exception as e:
            context.log.error(f"❌ Failed to add goal {goal_id} to pending: {e}")
            continue
    
    context.log.info(f"🎯 Added {added_count} new goals to pending")
    
    return {
        "status": "success",
        "added_count": added_count,
        "goal_ids": [g["goal_id"] for g in new_goals]
    }
