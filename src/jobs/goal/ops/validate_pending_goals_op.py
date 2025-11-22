"""Validate pending goals and move to confirmed"""
from datetime import datetime, timezone
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="validate_pending_goals",
    description="Validate pending goals that still exist in API and move to confirmed",
    tags={"kind": "database", "purpose": "validation"}
)
def validate_pending_goals_op(
    context: OpExecutionContext,
    goal_status: Dict[str, Any]
) -> Dict[str, Any]:
    """
    For goals that are in pending and still exist in the API:
    - They are now VALIDATED
    - Move them from goals_pending to goals_confirmed
    - These will trigger twitter_job
    
    Args:
        goal_status: Dict containing pending_goals list
        
    Returns:
        Dict with count of validated goals
    """
    pending_goals = goal_status.get("pending_goals", [])
    
    if not pending_goals:
        context.log.info("✅ No pending goals to validate")
        return {"status": "success", "validated_count": 0, "validated_goal_ids": []}
    
    store = FootyMongoStore()
    fixture_id = goal_status["fixture_id"]
    
    validated_count = 0
    validated_goal_ids = []
    
    for goal_data in pending_goals:
        goal_id = goal_data["goal_id"]
        event = goal_data["event"]
        pending_doc = goal_data["pending_doc"]
        
        try:
            # Create confirmed goal document
            confirmed_doc = {
                "_id": goal_id,
                "fixture_id": fixture_id,
                "event_data": event,
                "created_at": pending_doc.get("created_at"),
                "confirmed_at": datetime.now(timezone.utc).isoformat(),
                "validation_attempts": pending_doc.get("validation_attempts", 0) + 1
            }
            
            # Move to confirmed
            store.goals_confirmed.insert_one(confirmed_doc)
            
            # Remove from pending
            store.goals_pending.delete_one({"_id": goal_id})
            
            player = event.get("player", {}).get("name", "Unknown")
            team = event.get("team", {}).get("name", "Unknown")
            minute = event.get("time", {}).get("elapsed", "?")
            extra = event.get("time", {}).get("extra")
            minute_str = f"{minute}+{extra}" if extra else str(minute)
            
            context.log.info(f"✅ VALIDATED: {goal_id} - {player} ({team}) {minute_str}' → confirmed")
            validated_count += 1
            validated_goal_ids.append(goal_id)
            
        except Exception as e:
            context.log.error(f"❌ Failed to validate goal {goal_id}: {e}")
            continue
    
    context.log.info(f"🎯 Validated {validated_count} goals (pending → confirmed)")
    
    return {
        "status": "success",
        "validated_count": validated_count,
        "validated_goal_ids": validated_goal_ids
    }
