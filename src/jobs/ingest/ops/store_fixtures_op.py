"""Store fixtures in appropriate MongoDB collections"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="store_fixtures",
    description="Store categorized fixtures in appropriate collections (staging/active/completed)",
    tags={"kind": "database", "target": "mongodb"}
)
def store_fixtures_op(
    context: OpExecutionContext, 
    categorized_fixtures: Dict[str, List[Dict[str, Any]]]
) -> Dict[str, Any]:
    """
    Store fixtures in their appropriate collections based on categorization.
    
    Args:
        categorized_fixtures: Dict with keys 'staging', 'active', 'completed'
        
    Returns:
        Result dict with counts per collection
    """
    store = FootyMongoStore()
    
    staging_fixtures = categorized_fixtures.get("staging", [])
    active_fixtures = categorized_fixtures.get("active", [])
    completed_fixtures = categorized_fixtures.get("completed", [])
    
    total_fixtures = len(staging_fixtures) + len(active_fixtures) + len(completed_fixtures)
    
    if total_fixtures == 0:
        context.log.warning("⚠️  No fixtures to store")
        return {
            "status": "success",
            "staging_count": 0,
            "active_count": 0,
            "completed_count": 0,
            "total_count": 0
        }
    
    context.log.info(f"💾 Storing {total_fixtures} fixtures across collections")
    
    try:
        # Store in appropriate collections
        staging_count = store.bulk_insert_fixtures(staging_fixtures, "fixtures_staging") if staging_fixtures else 0
        active_count = store.bulk_insert_fixtures(active_fixtures, "fixtures_active") if active_fixtures else 0
        completed_count = store.bulk_insert_fixtures(completed_fixtures, "fixtures_completed") if completed_fixtures else 0
        
        context.log.info(f"✅ Stored fixtures: {staging_count} staging, {active_count} active, {completed_count} completed")
        
        return {
            "status": "success",
            "staging_count": staging_count,
            "active_count": active_count,
            "completed_count": completed_count,
            "total_count": staging_count + active_count + completed_count
        }
    
    except Exception as e:
        context.log.error(f"❌ Failed to store fixtures: {e}")
        raise
