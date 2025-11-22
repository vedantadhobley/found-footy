"""Update fixture data and complete if ready"""
from datetime import datetime
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="update_fixture",
    description="Update fixture data in fixtures_active and complete if ready",
    required_resource_keys={"mongo_store"},
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
    store: FootyMongoStore = context.resources.mongo_store
    
    fixture_id = process_result["fixture_id"]
    fixture_status = process_result["fixture_status"]
    goals_confirmed = process_result["goals_confirmed"]
    
    context.log.info(f"Updating fixture {fixture_id}")
    
    # Count goals for this fixture
    confirmed_count = store.db["goals_confirmed"].count_documents({"fixture_id": fixture_id})
    pending_count = store.db["goals_pending"].count_documents({"fixture_id": fixture_id})
    
    # Only update fixture if goals were confirmed (not if just added to pending)
    fixture_updated = False
    if goals_confirmed > 0:
        store.db["fixtures_active"].update_one(
            {"_id": fixture_id},
            {"$set": {
                "goals.stored": confirmed_count,
                "last_goal_update": datetime.utcnow()
            }}
        )
        context.log.info(f"📊 Updated fixture {fixture_id} stored goal count to {confirmed_count}")
        fixture_updated = True
    
    # Check if fixture should be completed
    completed_statuses = ["FT", "AET", "PEN", "ABD", "AWD", "WO", "CANC", "PST"]
    fixture_completed = False
    
    if fixture_status in completed_statuses and pending_count == 0:
        # Move to completed
        fixture_doc = store.db["fixtures_active"].find_one({"_id": fixture_id})
        if fixture_doc:
            # Update with final data
            fixture_doc["status"] = fixture_status
            fixture_doc["completed_at"] = datetime.utcnow()
            
            store.db["fixtures_completed"].insert_one(fixture_doc)
            store.db["fixtures_active"].delete_one({"_id": fixture_id})
            
            context.log.info(f"🏁 COMPLETED: Moved fixture {fixture_id} to completed ({fixture_status})")
            fixture_completed = True
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
