"""Filter out already-confirmed goals"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="filter_confirmed_goals",
    description="Remove goals that are already in goals_confirmed",
    required_resource_keys={"mongo_store"},
)
def filter_confirmed_goals_op(
    context: OpExecutionContext,
    fetch_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Generate goal_ids and filter out goals already in goals_confirmed.
    
    Returns:
        dict: {
            "fixture_id": int,
            "fixture_status": str,
            "new_goals": List[Dict] - goals not yet confirmed
        }
    """
    store: FootyMongoStore = context.resources.mongo_store
    
    fixture_id = fetch_result["fixture_id"]
    goal_events = fetch_result["goal_events"]
    
    context.log.info(f"Filtering confirmed goals for fixture {fixture_id}")
    
    new_goals = []
    filtered_count = 0
    
    for event in goal_events:
        player_id = event.get("player", {}).get("id")
        elapsed = event.get("time", {}).get("elapsed", 0)
        extra_time = event.get("time", {}).get("extra")
        
        if not player_id:
            context.log.warning("Goal event missing player_id, skipping")
            continue
        
        # Generate goal_id
        goal_id = f"{fixture_id}_{player_id}_{elapsed}"
        if extra_time:
            goal_id += f"+{extra_time}"
        
        # Check if already confirmed (by _id)
        if store.db["goals_confirmed"].find_one({"_id": goal_id}):
            context.log.debug(f"Goal {goal_id} already confirmed, filtering out")
            filtered_count += 1
            continue
        
        new_goals.append({
            "goal_id": goal_id,
            "player": event.get("player", {}),
            "team": event.get("team", {}),
            "time": event.get("time", {}),
            "detail": event.get("detail", "Normal Goal"),
        })
    
    context.log.info(f"Filtered {filtered_count} confirmed goals, {len(new_goals)} remaining to process")
    
    return {
        "fixture_id": fixture_id,
        "fixture_status": fetch_result["fixture_status"],
        "fixture_data": fetch_result["fixture_data"],
        "new_goals": new_goals
    }
