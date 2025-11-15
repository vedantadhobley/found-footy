"""Twitter Search Job - Search for goal videos on Twitter

This job:
1. Gets goal document from MongoDB
2. Searches Twitter via session service for videos
3. Updates goal with discovered_videos
4. Triggers download job if videos found
"""

from dagster import op, job, Config, OpExecutionContext, In, Out
from typing import Dict, Any, List, Optional
import requests
import os


class TwitterSearchConfig(Config):
    """Twitter search configuration"""
    mongo_uri: str = "mongodb://ffuser:ffpass@mongo:27017/found_footy?authSource=admin"
    db_name: str = "found_footy"
    twitter_session_url: str = "http://twitter-session:8888"
    max_results: int = 5


@op(
    name="search_twitter_for_goal",
    description="Search Twitter for videos of a specific goal",
    out=Out(Dict[str, Any])
)
def search_twitter_for_goal_op(context: OpExecutionContext, config: TwitterSearchConfig, goal_id: str) -> Dict[str, Any]:
    """Search Twitter for videos matching the goal
    
    Creates search query from player last name + team name
    Calls Twitter session service to find video tweets
    """
    from src.data.mongo_store import FootyMongoStore
    
    store = FootyMongoStore()
    
    # Get goal document
    goal_doc = store.goals.find_one({"_id": goal_id})
    if not goal_doc:
        context.log.warning(f"⚠️ Goal {goal_id} not found")
        return {"status": "goal_not_found", "goal_id": goal_id, "video_count": 0}
    
    # Extract player and team info from goal document
    player_name = goal_doc.get("player", {}).get("name", "")
    team_name = goal_doc.get("team", {}).get("name", "")
    
    if not player_name or not team_name:
        context.log.warning(f"⚠️ Missing player/team data for goal {goal_id}")
        # Still update with empty videos
        store.update_goal_processing_status(goal_id, "videos_discovered", discovered_videos=[])
        return {"status": "missing_data", "goal_id": goal_id, "video_count": 0, "videos": []}
    
    # Create search query (last name + team name)
    player_last_name = player_name.split()[-1] if " " in player_name else player_name
    search_query = f"{player_last_name} {team_name}"
    
    context.log.info(f"🔍 Searching Twitter for: '{search_query}' (Player: {player_name}, Team: {team_name})")
    
    # Call Twitter session service
    try:
        response = requests.post(
            f"{config.twitter_session_url}/search",
            json={"search_query": search_query, "max_results": config.max_results},
            timeout=60
        )
        
        if response.status_code == 200:
            data = response.json()
            found_videos = data.get("videos", [])
        else:
            context.log.warning(f"⚠️ Twitter search returned {response.status_code}")
            found_videos = []
            
    except requests.exceptions.ConnectionError:
        context.log.error("❌ Twitter session service unavailable")
        found_videos = []
    except Exception as e:
        context.log.error(f"❌ Twitter search failed: {e}")
        found_videos = []
    
    # Update goal with discovered videos
    store.update_goal_processing_status(
        goal_id,
        "videos_discovered",
        discovered_videos=found_videos,
        twitter_search_completed=True
    )
    
    context.log.info(f"✅ Twitter search complete: {len(found_videos)} videos for '{search_query}'")
    
    return {
        "status": "success",
        "goal_id": goal_id,
        "search_query": search_query,
        "player_name": player_name,
        "team_name": team_name,
        "video_count": len(found_videos),
        "videos": found_videos
    }


@op(
    name="trigger_download_if_videos_found",
    description="Trigger download job if videos were found",
    ins={"search_result": In(Dict[str, Any])}
)
def trigger_download_if_videos_found_op(context: OpExecutionContext, search_result: Dict[str, Any]) -> Dict[str, Any]:
    """Trigger download job if videos were discovered"""
    
    goal_id = search_result["goal_id"]
    video_count = search_result.get("video_count", 0)
    
    if video_count == 0:
        context.log.info(f"📭 No videos found for goal {goal_id}")
        return {"status": "no_videos", "goal_id": goal_id}
    
    context.log.info(f"✅ Found {video_count} videos - triggering download job")
    
    # Trigger download job
    from src.jobs.download_videos import download_and_upload_videos_job
    
    result = download_and_upload_videos_job.execute_in_process(
        run_config={
            "ops": {
                "validate_goal_and_get_videos": {
                    "config": {"goal_id": goal_id}
                }
            }
        },
        instance=context.instance,
        tags={
            "goal_id": goal_id,
            "triggered_by": "twitter_search"
        }
    )
    
    if result.success:
        context.log.info(f"✅ Download job completed for goal {goal_id}")
        return {"status": "download_triggered", "goal_id": goal_id, "success": True}
    else:
        context.log.error(f"❌ Download job failed for goal {goal_id}")
        return {"status": "download_failed", "goal_id": goal_id, "success": False}


@job(
    name="twitter_search_job",
    description="Search Twitter for goal videos and trigger download"
)
def twitter_search_job():
    """
    Twitter search job:
    
    1. Search Twitter for videos matching goal (player + team)
    2. Store discovered videos in goal document
    3. Trigger download job if videos found
    """
    search_result = search_twitter_for_goal_op()
    trigger_download_if_videos_found_op(search_result)


__all__ = ["twitter_search_job"]
