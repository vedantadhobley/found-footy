"""Update fixture data and complete if ready"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="update_fixture",
    description="Update fixture data in fixtures_active and complete if ready",
    required_resource_keys={"mongo"},
)
def update_fixture_op(
    context: OpExecutionContext,
    process_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Update fixture with confirmed goal count and complete if ready.
    
    Returns:
        dict: {
            "fixture_id": int,
            "confirmed_goal_ids": List[str],
            "fixture_updated": bool,
            "fixture_completed": bool
        }
    """
    store: FootyMongoStore = context.resources.mongo
    
    fixture_id = process_result["fixture_id"]
    fixture_status = process_result["fixture_status"]
    goals_confirmed = process_result["goals_confirmed"]
    
    context.log.info(f"Updating fixture {fixture_id}")
    
    # Count goals for this fixture
    confirmed_count = store.count_confirmed_goals_for_fixture(fixture_id)
    pending_count = store.count_pending_goals_for_fixture(fixture_id)
    
    # Only update fixture if goals were confirmed (not if just added to pending)
    fixture_updated = False
    if goals_confirmed > 0:
        fixture_updated = store.update_fixture_goal_counts(fixture_id)
        if fixture_updated:
            context.log.info(f"📊 Updated fixture {fixture_id} stored goal count to {confirmed_count}")
    
    # Check if fixture should be completed
    completed_statuses = ["FT", "AET", "PEN", "ABD", "AWD", "WO", "CANC", "PST"]
    fixture_completed = False
    
    if fixture_status in completed_statuses and pending_count == 0:
        # Move to completed
        fixture_completed = store.complete_fixture(fixture_id, fixture_status)
        if fixture_completed:
            context.log.info(f"🏁 COMPLETED: Moved fixture {fixture_id} to completed ({fixture_status})")
    else:
        if fixture_status in completed_statuses:
            context.log.info(
                f"⏳ Fixture {fixture_id} is {fixture_status} but has {pending_count} pending goals - "
                f"not completing yet"
            )
    
    return {
        "fixture_id": fixture_id,
        "confirmed_goal_ids": process_result["confirmed_goal_ids"],
        "fixture_updated": fixture_updated,
        "fixture_completed": fixture_completed,
        "confirmed_count": confirmed_count,
        "pending_count": pending_count
    }
