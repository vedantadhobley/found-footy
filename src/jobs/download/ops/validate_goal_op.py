"""Validate goal has discovered videos to download"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="validate_goal",
    description="Check goal exists and has discovered videos to download",
    tags={"kind": "validation", "purpose": "check"}
)
def validate_goal_op(context: OpExecutionContext, goal_id: str) -> Dict[str, Any]:
    """
    Validate goal exists in goals_confirmed and has discovered_videos.
    
    Args:
        goal_id: The goal ID
        
    Returns:
        Dict with goal data and validation status
    """
    store = FootyMongoStore()
    
    context.log.info(f"🔍 Validating goal {goal_id}")
    
    goal_doc = store.goals_confirmed.find_one({"_id": goal_id})
    
    if not goal_doc:
        context.log.warning(f"⚠️  Goal {goal_id} not found in goals_confirmed")
        return {"status": "not_found", "goal_id": goal_id}
    
    processing_status = goal_doc.get("processing_status", "unknown")
    
    if processing_status == "completed":
        context.log.info(f"⚠️  Goal {goal_id} already completed, skipping")
        return {"status": "already_completed", "goal_id": goal_id, "goal_doc": goal_doc}
    
    discovered_videos = goal_doc.get("discovered_videos", [])
    
    if not discovered_videos:
        context.log.warning(f"⚠️  No discovered videos for goal {goal_id}")
        
        # Mark as completed with no videos
        store.goals_confirmed.update_one(
            {"_id": goal_id},
            {
                "$set": {
                    "processing_status": "completed",
                    "successful_uploads": [],
                    "failed_downloads": []
                }
            }
        )
        
        return {"status": "no_videos", "goal_id": goal_id, "goal_doc": goal_doc}
    
    # Extract display info
    player_name = goal_doc.get("player", {}).get("name", "Unknown")
    team_name = goal_doc.get("team", {}).get("name", "Unknown")
    time_data = goal_doc.get("time", {})
    elapsed = time_data.get("elapsed", 0)
    extra = time_data.get("extra")
    minute_display = f"{elapsed}+{extra}" if extra else str(elapsed)
    
    context.log.info(
        f"✅ Goal validated: {team_name} - {player_name} ({minute_display}') "
        f"with {len(discovered_videos)} videos to download"
    )
    
    return {
        "status": "valid",
        "goal_id": goal_id,
        "goal_doc": goal_doc,
        "discovered_videos": discovered_videos,
        "player_name": player_name,
        "team_name": team_name,
        "minute_display": minute_display,
        "video_count": len(discovered_videos)
    }
