"""Update goal with completed download status"""
from datetime import datetime, timezone
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="update_goal_completed",
    description="Mark goal as completed with successful uploads",
    tags={"kind": "database", "purpose": "update"}
)
def update_goal_completed_op(
    context: OpExecutionContext,
    goal_id: str,
    successful_uploads: List[Dict[str, Any]],
    failed_downloads: List[Dict[str, Any]] | None = None
) -> Dict[str, Any]:
    """
    Update goal document with upload results.
    
    Sets processing_status to 'completed' and saves successful/failed uploads.
    
    Args:
        goal_id: The goal ID
        successful_uploads: List of successfully uploaded videos
        failed_downloads: List of failed download attempts
        
    Returns:
        Result dict with update status
    """
    store = FootyMongoStore()
    
    if failed_downloads is None:
        failed_downloads = []
    
    context.log.info(
        f"💾 Updating goal {goal_id}: {len(successful_uploads)} successful, "
        f"{len(failed_downloads)} failed"
    )
    
    try:
        result = store.goals_confirmed.update_one(
            {"_id": goal_id},
            {
                "$set": {
                    "processing_status": "completed",
                    "successful_uploads": successful_uploads,
                    "failed_downloads": failed_downloads,
                    "download_completed_at": datetime.now(timezone.utc).isoformat()
                }
            }
        )
        
        if result.modified_count > 0:
            context.log.info(f"✅ Goal {goal_id} marked as completed")
            return {
                "status": "success",
                "goal_id": goal_id,
                "successful_count": len(successful_uploads),
                "failed_count": len(failed_downloads)
            }
        else:
            context.log.warning(f"⚠️  Goal {goal_id} not found or not modified")
            return {
                "status": "not_found",
                "goal_id": goal_id
            }
    
    except Exception as e:
        context.log.error(f"❌ Failed to update goal {goal_id}: {e}")
        raise
