"""Spawn twitter jobs for validated goals"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

# Note: Will import twitter_job when we create it


@op(
    name="spawn_twitter_jobs",
    description="Trigger twitter_job for each validated goal",
    tags={"kind": "orchestration", "spawns": "twitter_job"}
)
def spawn_twitter_jobs_op(
    context: OpExecutionContext,
    validation_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    For each validated goal (moved from pending to confirmed),
    spawn a twitter_job to search for and download videos.
    
    Args:
        validation_result: Result from validate_pending_goals_op
        
    Returns:
        Dict with spawn statistics
    """
    validated_goal_ids = validation_result.get("validated_goal_ids", [])
    
    if not validated_goal_ids:
        context.log.info("✅ No twitter jobs to spawn")
        return {"status": "success", "spawned_count": 0}
    
    context.log.info(f"🚀 Spawning twitter jobs for {len(validated_goal_ids)} validated goals")
    
    spawned_count = 0
    
    for goal_id in validated_goal_ids:
        try:
            context.log.info(f"🚀 Marking goal {goal_id} for twitter_job")
            
            # Store goal_id in a collection for sensor to pick up
            from datetime import datetime

            from src.data.mongo_store import FootyMongoStore
            store = FootyMongoStore()
            
            # Store in pending_twitter_jobs collection
            store.db["pending_twitter_jobs"].insert_one({
                "goal_id": goal_id,
                "created_at": datetime.utcnow().isoformat()
            })
            
            spawned_count += 1
            
        except Exception as e:
            context.log.error(f"❌ Failed to spawn twitter job for goal {goal_id}: {e}")
            continue
    
    context.log.info(f"✅ Spawned {spawned_count} twitter jobs")
    
    return {
        "status": "success",
        "spawned_count": spawned_count,
        "goal_ids": validated_goal_ids
    }
