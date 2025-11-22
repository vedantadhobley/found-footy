"""Clean up invalidated goals from pending collection"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="cleanup_invalidated",
    description="Remove goals from pending that no longer exist in API (corrections/offsides)",
    tags={"kind": "database", "collection": "goals_pending"}
)
def cleanup_invalidated_goals_op(
    context: OpExecutionContext,
    goal_status: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Check if there are goals in goals_pending for this fixture
    that are NOT in the current API response.
    These goals have "disappeared" (offside, correction, etc.) and should be removed.
    
    Args:
        goal_status: Dict with fixture_id and all current goal IDs
        
    Returns:
        Dict with count of removed goals
    """
    store = FootyMongoStore()
    fixture_id = goal_status["fixture_id"]
    
    # Get all goal IDs from current API response
    current_goal_ids = set()
    for goal_list in ["confirmed_goals", "pending_goals", "new_goals"]:
        for goal_data in goal_status.get(goal_list, []):
            current_goal_ids.add(goal_data["goal_id"])
    
    # Find all pending goals for this fixture
    pending_docs = list(store.goals_pending.find({"fixture_id": fixture_id}))
    
    if not pending_docs:
        context.log.info("✅ No pending goals to check for invalidation")
        return {"status": "success", "removed_count": 0}
    
    removed_count = 0
    removed_goal_ids = []
    
    for pending_doc in pending_docs:
        goal_id = pending_doc["_id"]
        
        # If this pending goal is NOT in the current API response, remove it
        if goal_id not in current_goal_ids:
            try:
                store.goals_pending.delete_one({"_id": goal_id})
                
                event_data = pending_doc.get("event_data", {})
                player = event_data.get("player", {}).get("name", "Unknown")
                minute = event_data.get("time", {}).get("elapsed", "?")
                
                context.log.warning(
                    f"⚠️  INVALIDATED: {goal_id} removed from pending "
                    f"({player} {minute}' - no longer in API)"
                )
                removed_count += 1
                removed_goal_ids.append(goal_id)
                
            except Exception as e:
                context.log.error(f"❌ Failed to remove invalidated goal {goal_id}: {e}")
                continue
    
    if removed_count > 0:
        context.log.info(f"🗑️  Removed {removed_count} invalidated goals from pending")
    else:
        context.log.info("✅ All pending goals still valid")
    
    return {
        "status": "success",
        "removed_count": removed_count,
        "removed_goal_ids": removed_goal_ids
    }
