"""Execute goal changes: add, confirm, drop"""
from datetime import datetime
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="process_goal_changes",
    description="Execute goal changes: add to pending, confirm, or drop",
    required_resource_keys={"mongo_store"},
)
def process_goal_changes_op(
    context: OpExecutionContext,
    compare_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Execute the goal changes identified in comparison.
    
    Returns:
        dict: {
            "fixture_id": int,
            "confirmed_goal_ids": List[str] - for twitter job triggering
            "goals_added": int,
            "goals_confirmed": int,
            "goals_dropped": int
        }
    """
    store: FootyMongoStore = context.resources.mongo_store
    
    fixture_id = compare_result["fixture_id"]
    to_add = compare_result["to_add"]
    to_confirm = compare_result["to_confirm"]
    to_drop = compare_result["to_drop"]
    goal_data = compare_result["goal_data"]
    
    context.log.info(f"Processing goal changes for fixture {fixture_id}")
    
    # Add new goals to pending
    for goal_id in to_add:
        goal = goal_data[goal_id]
        goal_doc = {
            "_id": goal_id,
            "fixture_id": fixture_id,
            "player": goal["player"],
            "team": goal["team"],
            "time": goal["time"],
            "detail": goal["detail"],
            "first_seen": datetime.utcnow(),
            "status": "pending"
        }
        
        store.db["goals_pending"].insert_one(goal_doc)
        context.log.info(
            f"➕ ADDED to pending: {goal_id} - "
            f"{goal['player'].get('name')} @ {goal['time'].get('elapsed')}'"
        )
    
    # Confirm goals (move from pending to confirmed)
    confirmed_goal_ids = []
    for goal_id in to_confirm:
        goal_doc = store.db["goals_pending"].find_one({"_id": goal_id})
        if goal_doc:
            # Update with validation timestamp
            goal_doc["validated_at"] = datetime.utcnow()
            goal_doc["status"] = "confirmed"
            
            # Move to confirmed
            store.db["goals_confirmed"].insert_one(goal_doc)
            store.db["goals_pending"].delete_one({"_id": goal_id})
            
            goal = goal_data.get(goal_id, {})
            context.log.info(
                f"✅ CONFIRMED: {goal_id} - "
                f"{goal.get('player', {}).get('name', 'Unknown')} @ "
                f"{goal.get('time', {}).get('elapsed', '?')}'"
            )
            confirmed_goal_ids.append(goal_id)
    
    # Drop invalidated goals
    for goal_id in to_drop:
        store.db["goals_pending"].delete_one({"_id": goal_id})
        context.log.warning(f"❌ DROPPED from pending: {goal_id} (no longer in API)")
    
    context.log.info(
        f"Completed: +{len(to_add)} added, ✅{len(confirmed_goal_ids)} confirmed, "
        f"❌{len(to_drop)} dropped"
    )
    
    return {
        "fixture_id": fixture_id,
        "fixture_status": compare_result["fixture_status"],
        "fixture_data": compare_result["fixture_data"],
        "confirmed_goal_ids": confirmed_goal_ids,
        "goals_added": len(to_add),
        "goals_confirmed": len(confirmed_goal_ids),
        "goals_dropped": len(to_drop)
    }
