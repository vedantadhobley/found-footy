"""Execute goal changes: add, confirm, drop"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="process_goal_changes",
    description="Execute goal changes: add to pending, confirm, or drop",
    required_resource_keys={"mongo"},
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
    store: FootyMongoStore = context.resources.mongo
    
    fixture_id = compare_result["fixture_id"]
    to_add = compare_result["to_add"]
    to_confirm = compare_result["to_confirm"]
    to_drop = compare_result["to_drop"]
    goal_data = compare_result["goal_data"]
    
    context.log.info(f"Processing goal changes for fixture {fixture_id}")
    
    # Add new goals to pending
    for goal_id in to_add:
        goal = goal_data[goal_id]
        store.add_goal_to_pending(goal_id, fixture_id, goal)
        context.log.info(
            f"➕ ADDED to pending: {goal_id} - "
            f"{goal['player'].get('name')} @ {goal['time'].get('elapsed')}'"
        )
    
    # Confirm goals (move from pending to confirmed)
    confirmed_goal_ids = []
    for goal_id in to_confirm:
        if store.confirm_goal(goal_id):
            goal = goal_data.get(goal_id, {})
            context.log.info(
                f"✅ CONFIRMED: {goal_id} - "
                f"{goal.get('player', {}).get('name', 'Unknown')} @ "
                f"{goal.get('time', {}).get('elapsed', '?')}'"
            )
            confirmed_goal_ids.append(goal_id)
    
    # Drop invalidated goals
    for goal_id in to_drop:
        if store.drop_goal_from_pending(goal_id):
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
