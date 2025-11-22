"""Update goal with discovered video URLs"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="update_goal_discovered_videos",
    description="Save discovered video URLs to goal document in goals_confirmed",
    tags={"kind": "database", "purpose": "update"}
)
def update_goal_discovered_videos_op(
    context: OpExecutionContext,
    extraction_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Update goal document with discovered video URLs.
    
    Sets processing_status to 'discovered' and saves video metadata
    to discovered_videos field.
    
    Args:
        extraction_result: Dict with goal_id and videos from extract_videos_op
        
    Returns:
        Result dict with update status
    """
    store = FootyMongoStore()
    
    # Extract goal_id and video metadata
    goal_id = extraction_result.get("goal_id", "unknown")
    video_metadata = extraction_result.get("videos", [])
    
    if not video_metadata:
        context.log.warning(f"⚠️  No videos discovered for goal {goal_id}")
        
        # Update goal with empty discovered_videos
        store.goals_confirmed.update_one(
            {"_id": goal_id},
            {
                "$set": {
                    "discovered_videos": [],
                    "processing_status": "discovered",
                    "video_discovery_completed_at": context.run.tags.get("dagster/run_start_time")
                }
            }
        )
        
        return {
            "status": "no_videos",
            "goal_id": goal_id,
            "videos_count": 0
        }
    
    context.log.info(f"💾 Updating goal {goal_id} with {len(video_metadata)} discovered videos")
    
    try:
        result = store.goals_confirmed.update_one(
            {"_id": goal_id},
            {
                "$set": {
                    "discovered_videos": video_metadata,
                    "processing_status": "discovered",
                    "video_discovery_completed_at": context.run.tags.get("dagster/run_start_time")
                }
            }
        )
        
        if result.modified_count > 0:
            context.log.info(f"✅ Updated goal {goal_id} with {len(video_metadata)} video URLs")
            return {
                "status": "success",
                "goal_id": goal_id,
                "videos_count": len(video_metadata)
            }
        else:
            context.log.warning(f"⚠️  Goal {goal_id} not found or not modified")
            return {
                "status": "not_found",
                "goal_id": goal_id,
                "videos_count": len(video_metadata)
            }
    
    except Exception as e:
        context.log.error(f"❌ Failed to update goal {goal_id}: {e}")
        raise
