"""Check goal status in pending/confirmed collections"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


def generate_goal_id(fixture_id: int, goal_event: Dict[str, Any]) -> str:
    """
    Generate a unique goal ID from fixture and event data.
    Format: {fixture_id}_{player_id}_{elapsed}[+{extra}]
    
    Including player_id allows:
    - Direct _id comparison for delta detection (no collection scans)
    - Handles edge case of 2 goals at exact same minute by different players
    - Faster MongoDB lookups with exact _id match
    
    Args:
        fixture_id: The fixture ID
        goal_event: The goal event data containing player, time info
        
    Returns:
        Goal ID string
    """
    time_data = goal_event.get("time", {})
    elapsed = time_data.get("elapsed", 0)
    extra = time_data.get("extra")
    
    # Extract player ID
    player_id = goal_event.get("player", {}).get("id")
    
    if not player_id:
        # Fallback if player_id missing (shouldn't happen for valid goals)
        player_id = "unknown"
    
    if extra is not None and extra > 0:
        return f"{fixture_id}_{player_id}_{elapsed}+{extra}"
    else:
        return f"{fixture_id}_{player_id}_{elapsed}"


@op(
    name="check_goal_status",
    description="Check if goal exists in pending or confirmed collections",
    tags={"kind": "database", "purpose": "validation"}
)
def check_goal_status_op(
    context: OpExecutionContext,
    fixture_id: int,
    goal_events: list[Dict[str, Any]]
) -> Dict[str, Any]:
    """
    For each goal event, check its status:
    - In goals_confirmed: Already processed, skip
    - In goals_pending: Needs validation
    - In neither: New goal, needs to be added to pending
    
    Args:
        fixture_id: The fixture ID
        goal_events: List of goal events from API
        
    Returns:
        Dict categorizing goals by status
    """
    store = FootyMongoStore()
    
    confirmed_goals = []
    pending_goals = []
    new_goals = []
    
    for goal_event in goal_events:
        goal_id = generate_goal_id(fixture_id, goal_event)
        
        # Check if in confirmed collection
        confirmed_doc = store.goals_confirmed.find_one({"_id": goal_id})
        if confirmed_doc:
            context.log.info(f"✅ Goal {goal_id} already confirmed - skipping")
            confirmed_goals.append({
                "goal_id": goal_id,
                "event": goal_event
            })
            continue
        
        # Check if in pending collection
        pending_doc = store.goals_pending.find_one({"_id": goal_id})
        if pending_doc:
            context.log.info(f"🔄 Goal {goal_id} in pending - needs validation")
            pending_goals.append({
                "goal_id": goal_id,
                "event": goal_event,
                "pending_doc": pending_doc
            })
            continue
        
        # New goal
        context.log.info(f"🆕 Goal {goal_id} is new - will add to pending")
        new_goals.append({
            "goal_id": goal_id,
            "event": goal_event
        })
    
    context.log.info(
        f"📊 Goal status: {len(confirmed_goals)} confirmed, "
        f"{len(pending_goals)} pending, {len(new_goals)} new"
    )
    
    return {
        "fixture_id": fixture_id,
        "confirmed_goals": confirmed_goals,
        "pending_goals": pending_goals,
        "new_goals": new_goals
    }
