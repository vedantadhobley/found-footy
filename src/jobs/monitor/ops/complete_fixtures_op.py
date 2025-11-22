"""Complete fixtures that are finished and have no pending goals"""
from datetime import datetime, timezone
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore
from src.utils.fixture_status import get_completed_statuses


@op(
    name="complete_fixtures",
    description="Move finished fixtures to completed collection if all goals are validated",
    tags={"kind": "database", "collection": "fixtures"}
)
def complete_fixtures_op(
    context: OpExecutionContext,
    fixtures_with_new_goals: List[Dict[str, Any]],
    fresh_fixtures: List[Dict[str, Any]]
) -> Dict[str, Any]:
    """
    Check for fixtures that are finished (FT status).
    
    CRITICAL: A fixture can ONLY move to completed if:
    1. Fixture status is "FT" (finished)
    2. THIS SPECIFIC FIXTURE has NO goals in goals_pending
    
    Even if the match is finished, we MUST keep polling it in active collection
    until all goals are validated (moved to confirmed) or dropped (invalidated).
    
    This ensures we don't lose late-game goals that appear after FT status.
    
    Args:
        fixtures_with_new_goals: Fixtures with goal deltas (for dependency ordering)
        fresh_fixtures: Fresh fixture data from API
        
    Returns:
        Dict with completion statistics
    """
    store = FootyMongoStore()
    
    # Get all completion statuses from fixture_status.py
    # Includes: FT, AET, PEN, PST, CANC, ABD, AWD, WO
    completed_statuses = get_completed_statuses()
    
    # Build set of fixture IDs that have new goals (DON'T complete these yet!)
    fixtures_with_new_goal_ids = {f["fixture_id"] for f in fixtures_with_new_goals}
    
    if fixtures_with_new_goal_ids:
        context.log.info(
            f"📊 {len(fixtures_with_new_goal_ids)} fixtures have new goals - "
            f"will not complete these until goals are in pending"
        )
    
    completed_count = 0
    skipped_pending_goals = 0
    skipped_new_goals = 0
    
    for fresh_fixture in fresh_fixtures:
        try:
            fixture_id = fresh_fixture["fixture"]["id"]
            status = fresh_fixture.get("fixture", {}).get("status", {}).get("short", "")
            
            # Only process finished fixtures (any completion status)
            if status not in completed_statuses:
                continue
            
            # CRITICAL: Don't complete if this fixture has NEW goals we just detected
            # The goal job will add them to pending, but that happens async
            # We need to wait for next monitor cycle to check pending count
            if fixture_id in fixtures_with_new_goal_ids:
                context.log.warning(
                    f"⚠️  CANNOT COMPLETE: Fixture {fixture_id} has NEW goals detected this cycle "
                    f"(goal job spawned). Will check pending on next monitor cycle."
                )
                skipped_new_goals += 1
                continue
            
            context.log.info(f"🏁 Checking fixture {fixture_id} for completion (status: {status})")
            
            # CRITICAL: Check if THIS SPECIFIC FIXTURE has any pending goals
            # We only check goals for this fixture_id, not all pending goals
            pending_goals_count = store.goals_pending.count_documents({"fixture_id": fixture_id})
            
            if pending_goals_count > 0:
                context.log.warning(
                    f"⚠️  CANNOT COMPLETE: Fixture {fixture_id} has {pending_goals_count} goals "
                    f"in pending (awaiting validation). Will keep polling in active collection."
                )
                skipped_pending_goals += 1
                continue
            
            # Check if fixture exists in active collection
            stored_fixture = store.fixtures_active.find_one({"_id": fixture_id})
            
            if not stored_fixture:
                context.log.warning(f"⚠️  Fixture {fixture_id} not found in active collection")
                continue
            
            # Update with fresh data and move to completed
            completed_fixture = dict(fresh_fixture)
            completed_fixture["_id"] = fixture_id
            completed_fixture["completed_at"] = datetime.now(timezone.utc).isoformat()
            
            # Insert into completed collection
            store.fixtures_completed.replace_one(
                {"_id": fixture_id},
                completed_fixture,
                upsert=True
            )
            
            # Remove from active collection (safe now - all goals validated)
            store.fixtures_active.delete_one({"_id": fixture_id})
            
            home_team = fresh_fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
            away_team = fresh_fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
            home_goals = fresh_fixture.get("goals", {}).get("home", 0)
            away_goals = fresh_fixture.get("goals", {}).get("away", 0)
            
            context.log.info(
                f"✅ COMPLETED: Fixture {fixture_id} ({home_team} {home_goals}-{away_goals} {away_team}) "
                f"- All goals validated, moved to completed collection"
            )
            completed_count += 1
        
        except Exception as e:
            context.log.error(f"❌ Error completing fixture: {e}")
            continue
    
    context.log.info(
        f"🎯 Completed {completed_count} fixtures "
        f"(skipped {skipped_new_goals} with new goals, {skipped_pending_goals} with pending goals)"
    )
    
    return {
        "status": "success",
        "completed_count": completed_count,
        "skipped_pending_goals": skipped_pending_goals
    }
