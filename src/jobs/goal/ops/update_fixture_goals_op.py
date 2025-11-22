"""Update fixture goal count after validation"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="update_fixture_goals",
    description="Update fixture goal count in active collection after goals are validated",
    tags={"kind": "database", "collection": "fixtures_active"}
)
def update_fixture_goals_op(
    context: OpExecutionContext,
    fixture_id: int,
    validation_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    After goals are validated and moved to confirmed, update the fixture
    with the current goal count from the API.
    
    This ensures the fixture data reflects reality only after validation.
    
    Args:
        fixture_id: The fixture ID to update
        validation_result: Result from validation op
        
    Returns:
        Dict with update status
    """
    validated_count = validation_result.get("validated_count", 0)
    
    if validated_count == 0:
        context.log.info("ℹ️  No goals validated, no fixture update needed")
        return {"status": "no_update", "updated": False}
    
    store = FootyMongoStore()
    
    try:
        # Get the fixture from active collection
        fixture = store.fixtures_active.find_one({"_id": fixture_id})
        
        if not fixture:
            context.log.warning(f"⚠️  Fixture {fixture_id} not found in active collection")
            return {"status": "not_found", "updated": False}
        
        # Fetch fresh fixture data from API
        from datetime import datetime

        from src.api.mongo_api import fixtures_batch
        
        context.log.info(f"🔄 Fetching fresh fixture data for {fixture_id}")
        
        fresh_fixtures = fixtures_batch([fixture_id])
        
        if not fresh_fixtures:
            context.log.error(f"❌ Could not fetch fresh data for fixture {fixture_id}")
            return {"status": "fetch_failed", "updated": False}
        
        fresh_fixture = fresh_fixtures[0]
        
        # Update fixture with fresh goal data
        store.fixtures_active.update_one(
            {"_id": fixture_id},
            {"$set": {
                "goals": fresh_fixture.get("goals", {}),
                "score": fresh_fixture.get("score", {}),
                "updated_at": datetime.utcnow().isoformat()
            }}
        )
        
        context.log.info(
            f"✅ Updated fixture {fixture_id} with fresh goal data "
            f"({validated_count} goals validated)"
        )
        
        return {
            "status": "success",
            "updated": True,
            "validated_goals": validated_count
        }
        
    except Exception as e:
        context.log.error(f"❌ Failed to update fixture {fixture_id}: {e}")
        raise
