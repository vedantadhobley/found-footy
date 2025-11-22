"""Categorize fixtures by status into staging/active/completed"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.utils.fixture_status import (
    get_active_statuses,
    get_completed_statuses,
    get_staging_statuses,
)


@op(
    name="categorize_fixtures",
    description="Route fixtures to correct collection based on status",
    tags={"kind": "logic", "purpose": "routing"}
)
def categorize_fixtures_op(
    context: OpExecutionContext,
    fixtures_data: List[Dict[str, Any]]
) -> Dict[str, List[Dict[str, Any]]]:
    """
    Categorize fixtures based on their status code.
    
    Uses fixture_status.py to determine which collection each fixture belongs in:
    - staging: TBD, NS (not started)
    - active: 1H, HT, 2H, ET, BT, P, SUSP, INT, LIVE (in progress)
    - completed: FT, AET, PEN, PST, CANC, ABD, AWD, WO (finished)
    
    This optimization means:
    - Fixtures already finished go straight to completed (no wasted monitor cycles)
    - Fixtures already live go straight to active (catch goals immediately!)
    - Monitor only processes what's actually in progress
    
    Args:
        fixtures_data: List of fixture objects from API
        
    Returns:
        Dict with keys 'staging', 'active', 'completed' containing categorized fixtures
    """
    if not fixtures_data:
        context.log.warning("⚠️  No fixtures to categorize")
        return {"staging": [], "active": [], "completed": []}
    
    # Get status sets from fixture_status.py
    completed_statuses = set(get_completed_statuses())
    active_statuses = set(get_active_statuses())
    staging_statuses = set(get_staging_statuses())
    
    staging_fixtures = []
    active_fixtures = []
    completed_fixtures = []
    unknown_status_fixtures = []
    
    for fixture in fixtures_data:
        try:
            status = fixture.get("fixture", {}).get("status", {}).get("short", "")
            fixture_id = fixture.get("fixture", {}).get("id", "unknown")
            
            if not status:
                context.log.warning(f"⚠️  Fixture {fixture_id} has no status, defaulting to staging")
                staging_fixtures.append(fixture)
                continue
            
            # Route based on status
            if status in completed_statuses:
                completed_fixtures.append(fixture)
            elif status in active_statuses:
                active_fixtures.append(fixture)
            elif status in staging_statuses:
                staging_fixtures.append(fixture)
            else:
                context.log.warning(f"⚠️  Unknown status '{status}' for fixture {fixture_id}, defaulting to staging")
                unknown_status_fixtures.append(fixture)
                staging_fixtures.append(fixture)
        
        except Exception as e:
            context.log.error(f"❌ Error categorizing fixture: {e}")
            staging_fixtures.append(fixture)
            continue
    
    context.log.info(
        f"📊 Categorized {len(fixtures_data)} fixtures: "
        f"{len(staging_fixtures)} staging, "
        f"{len(active_fixtures)} active, "
        f"{len(completed_fixtures)} completed"
    )
    
    if unknown_status_fixtures:
        context.log.warning(
            f"⚠️  {len(unknown_status_fixtures)} fixtures with unknown status codes"
        )
    
    if active_fixtures:
        context.log.info(
            f"🔥 {len(active_fixtures)} fixtures already LIVE - will catch goals immediately!"
        )
    
    if completed_fixtures:
        context.log.info(
            f"🏁 {len(completed_fixtures)} fixtures already FINISHED - skip monitoring"
        )
    
    return {
        "staging": staging_fixtures,
        "active": active_fixtures,
        "completed": completed_fixtures
    }
