"""Activate fixtures that have reached their start time"""
from datetime import datetime, timezone
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="activate_fixtures",
    description="Move fixtures from staging to active when their start time is reached",
    tags={"kind": "database", "collection": "fixtures"}
)
def activate_fixtures_op(context: OpExecutionContext) -> Dict[str, Any]:
    """
    Check fixtures_staging for fixtures that should start.
    Move them to fixtures_active if start time has been reached.
    
    Returns:
        Dict with count of activated fixtures
    """
    store = FootyMongoStore()
    now = datetime.now(timezone.utc)
    
    context.log.info(f"🕐 Checking for fixtures to activate (current time: {now.isoformat()})")
    
    # Get all fixtures from staging
    staging_fixtures = store.get_staging_fixtures()
    
    if not staging_fixtures:
        context.log.info("📋 No fixtures in staging")
        return {"status": "success", "activated_count": 0}
    
    context.log.info(f"📋 Found {len(staging_fixtures)} fixtures in staging")
    
    activated_count = 0
    
    for fixture in staging_fixtures:
        try:
            fixture_id = fixture["_id"]
            fixture_date_str = fixture.get("fixture", {}).get("date")
            
            if not fixture_date_str:
                continue
            
            # Parse fixture date
            fixture_date = datetime.fromisoformat(fixture_date_str.replace('Z', '+00:00'))
            
            # Check if fixture should be active (start time reached)
            if fixture_date <= now:
                # Move to active
                if store.activate_fixture(fixture_id, fixture):
                    home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
                    away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
                    context.log.info(f"✅ Activated fixture {fixture_id}: {home_team} vs {away_team}")
                    activated_count += 1
        
        except Exception as e:
            context.log.error(f"❌ Error activating fixture {fixture.get('_id')}: {e}")
            continue
    
    context.log.info(f"🎯 Activated {activated_count} fixtures")
    
    return {
        "status": "success",
        "activated_count": activated_count,
        "total_staging": len(staging_fixtures)
    }
