"""Spawn goal jobs for fixtures with new goals"""
from datetime import datetime
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

# Note: We'll import the goal_job when we create it
# For now, this is a placeholder that will trigger goal jobs


@op(
    name="spawn_goal_jobs",
    description="Trigger goal_job for each fixture with new goals",
    tags={"kind": "orchestration", "spawns": "goal_job"}
)
def spawn_goal_jobs_op(
    context: OpExecutionContext,
    fixtures_with_new_goals: List[Dict[str, Any]]
) -> Dict[str, Any]:
    """
    For each fixture with new goals, spawn a goal_job to process them.
    
    Args:
        fixtures_with_new_goals: List of fixtures with goal deltas
        
    Returns:
        Dict with spawn statistics
    """
    if not fixtures_with_new_goals:
        context.log.info("✅ No goal jobs to spawn")
        return {"status": "success", "spawned_count": 0}
    
    context.log.info(f"🚀 Spawning goal jobs for {len(fixtures_with_new_goals)} fixtures")
    
    spawned_count = 0
    
    for fixture_data in fixtures_with_new_goals:
        try:
            fixture_id = fixture_data["fixture_id"]
            goal_delta = fixture_data["goal_delta"]
            
            context.log.info(f"🚀 Marking fixture {fixture_id} for goal_job ({goal_delta} new goals)")
            
            # Store fixture_id in a collection for sensor to pick up
            from src.data.mongo_store import FootyMongoStore
            store = FootyMongoStore()
            
            # Store in pending_goal_jobs collection
            store.db["pending_goal_jobs"].insert_one({
                "fixture_id": fixture_id,
                "goal_delta": goal_delta,
                "created_at": datetime.utcnow().isoformat()
            })
            
            spawned_count += 1
            
        except Exception as e:
            context.log.error(f"❌ Failed to spawn goal job for fixture {fixture_data.get('fixture_id')}: {e}")
            continue
    
    context.log.info(f"✅ Spawned {spawned_count} goal jobs")
    
    return {
        "status": "success",
        "spawned_count": spawned_count,
        "total_fixtures": len(fixtures_with_new_goals)
    }
