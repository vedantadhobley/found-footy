"""Monitor fixtures asset - checks active fixtures for goal changes"""

import logging
from dagster import asset, AssetExecutionContext, Output
from src.utils import fixture_logic
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


@asset(
    name="monitor_fixtures",
    description="Monitor active fixtures for goal changes and trigger processing",
    group_name="fixtures",
    compute_kind="monitoring"
)
def monitor_fixtures_asset(context: AssetExecutionContext) -> Output:
    """
    Monitor fixtures asset - migrated from monitor_flow.py
    
    Steps:
    1. Get active fixtures from MongoDB
    2. Detect changes (goal deltas) since last check
    3. Trigger goal processing for fixtures with changes
    """
    store = FootyMongoStore()
    
    context.log.info("🔍 Monitoring active fixtures for changes")
    
    # Detect fixture deltas (goal changes)
    delta_results = fixture_logic.detect_fixture_deltas(store)
    
    if delta_results["status"] != "success":
        context.log.error("❌ Delta detection failed")
        return Output(
            value={"status": "error", "delta_results": delta_results},
            metadata={"error": str(delta_results)}
        )
    
    fixtures_with_changes = delta_results["fixtures_with_changes"]
    total_goals = delta_results["total_goals_detected"]
    
    context.log.info(f"🔍 Found {len(fixtures_with_changes)} fixtures with changes, {total_goals} total goals")
    
    result = {
        "status": "success",
        "fixtures_monitored": len(fixtures_with_changes),
        "total_goals_detected": total_goals,
        "fixtures_with_changes": [f["fixture_id"] for f in fixtures_with_changes]
    }
    
    return Output(
        value=result,
        metadata={
            "fixtures_monitored": len(fixtures_with_changes),
            "goals_detected": total_goals
        }
    )
