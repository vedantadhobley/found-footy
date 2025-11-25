"""Batch fetch current data for all active fixtures"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.api.mongo_api import fixtures_batch
from src.data.mongo_store import FootyMongoStore


@op(
    name="batch_fetch_active",
    description="Fetch current data for all active fixtures from api-football.com",
    tags={"kind": "api", "source": "api-football"}
)
def batch_fetch_active_op(
    context: OpExecutionContext,
    activate_result: Dict[str, Any]
) -> List[Dict[str, Any]]:
    """
    Get all active fixture IDs and batch fetch their current data from API.
    
    Args:
        activate_result: Result from activate_fixtures_op (for dependency ordering)
    
    Returns:
        List of fresh fixture data from API
    """
    # activate_result is used only for dependency ordering
    context.log.info(f"📊 Activated {activate_result.get('activated_count', 0)} fixtures")
    store = FootyMongoStore()
    
    # Get all active fixture IDs
    fixture_ids = store.get_active_fixture_ids()
    
    if not fixture_ids:
        context.log.info("📋 No active fixtures to fetch")
        return []
    context.log.info(f"🌐 Batch fetching data for {len(fixture_ids)} active fixtures")
    
    try:
        fresh_data = fixtures_batch(fixture_ids)
        context.log.info(f"✅ Retrieved fresh data for {len(fresh_data)} fixtures")
        
        return fresh_data
    
    except Exception as e:
        context.log.error(f"❌ Failed to batch fetch fixtures: {e}")
        raise
