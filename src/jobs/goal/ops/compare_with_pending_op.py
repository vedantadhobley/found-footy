"""Compare new goals with goals_pending for fixture"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="compare_with_pending",
    description="Compare new goals with goals_pending for this fixture",
    required_resource_keys={"mongo"},
)
def compare_with_pending_op(
    context: OpExecutionContext,
    filter_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Compare new goals with goals_pending to determine actions.
    
    Returns:
        dict: {
            "fixture_id": int,
            "to_add": List[str] - goal_ids to add to pending
            "to_confirm": List[str] - goal_ids to move to confirmed
            "to_drop": List[str] - goal_ids to drop from pending
            "goal_data": Dict[str, Dict] - goal metadata by goal_id
        }
    """
    store: FootyMongoStore = context.resources.mongo
    
    fixture_id = filter_result["fixture_id"]
    new_goals = filter_result["new_goals"]
    
    context.log.info(f"Comparing {len(new_goals)} goals with pending for fixture {fixture_id}")
    
    # Get all goals_pending for THIS fixture
    pending_goals = store.get_pending_goals_for_fixture(fixture_id)
    pending_goal_ids = {g["_id"] for g in pending_goals}
    
    context.log.info(f"Found {len(pending_goals)} goals in pending for fixture {fixture_id}")
    
    # Build sets for comparison
    fetched_goal_ids = {g["goal_id"] for g in new_goals}
    goal_data = {g["goal_id"]: g for g in new_goals}
    
    # Determine actions
    to_confirm = list(fetched_goal_ids & pending_goal_ids)  # In both
    to_add = list(fetched_goal_ids - pending_goal_ids)  # In fetched, not in pending
    to_drop = list(pending_goal_ids - fetched_goal_ids)  # In pending, not in fetched
    
    context.log.info(
        f"Actions: +{len(to_add)} add to pending, "
        f"✅{len(to_confirm)} confirm, "
        f"❌{len(to_drop)} drop from pending"
    )
    
    return {
        "fixture_id": fixture_id,
        "fixture_status": filter_result["fixture_status"],
        "fixture_data": filter_result["fixture_data"],
        "to_add": to_add,
        "to_confirm": to_confirm,
        "to_drop": to_drop,
        "goal_data": goal_data,
        "pending_count_before": len(pending_goals)
    }
