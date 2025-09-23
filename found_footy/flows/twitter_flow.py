"""Clean Twitter Flow - Simple session handling"""
import os
import requests
from typing import Optional, List, Dict, Any
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment

from found_footy.storage.mongo_store import FootyMongoStore

class TwitterAPIClient:
    """Simple Twitter session client"""
    
    def __init__(self):
        self.session_url = os.getenv('TWITTER_SESSION_URL', 'http://twitter-session:8888')
        
    def search_videos(self, search_query: str, max_results: int = 3) -> List[Dict[str, Any]]:
        """Search videos via session service - fail if unavailable"""
        try:
            response = requests.post(
                f"{self.session_url}/search",
                json={"search_query": search_query, "max_results": max_results},
                timeout=60
            )
            
            if response.status_code == 200:
                data = response.json()
                videos = data.get("videos", [])
                return videos
            else:
                return []
                
        except requests.exceptions.ConnectionError:
            return []
        except Exception:
            return []

@task(name="twitter-search-task")
def twitter_search_task(goal_id: str) -> Dict[str, Any]:
    """Simple Twitter search - find videos or don't"""
    logger = get_run_logger()
    logger.info(f"🔍 Twitter search for goal: {goal_id}")

    store = FootyMongoStore()
    
    goal_doc = store.goals_pending.find_one({"_id": goal_id})
    if not goal_doc:
        logger.warning(f"⚠️ Goal {goal_id} not found")
        return {"status": "goal_not_found", "goal_id": goal_id}

    player_name = goal_doc.get("player_name", "")
    team_name = goal_doc.get("team_name", "")
    player_last_name = player_name.split()[-1] if " " in player_name else player_name
    search_query = f"{player_last_name} {team_name}"

    client = TwitterAPIClient()
    found_videos = client.search_videos(search_query, max_results=3)

    # Update goal with discovered videos (empty list if none found)
    goal_doc["discovered_videos"] = found_videos
    store.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)

    logger.info(f"✅ Twitter search complete: {len(found_videos)} videos for {search_query}")
    return {
        "status": "success",
        "goal_id": goal_id,
        "discovered_videos": found_videos,
        "video_count": len(found_videos)
    }

@flow(name="twitter-flow")
def twitter_flow(goal_id: Optional[str] = None):
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}

    logger.info(f"🔍 Starting Twitter search for goal: {goal_id}")
    result = twitter_search_task(goal_id)

    if result["status"] == "success" and result.get("video_count", 0) > 0:
        logger.info(f"✅ Found {result['video_count']} videos - triggering download")
        
        try:
            run_deployment(
                name="download-flow/download-flow",
                parameters={"goal_id": goal_id},
                flow_run_name=f"📥 DOWNLOAD: {goal_id} ({result['video_count']} videos)"
            )
            next_step = "download_triggered"
        except Exception as e:
            logger.error(f"❌ Failed to trigger download: {e}")
            next_step = "download_failed"
    else:
        logger.info(f"📭 No videos found for goal {goal_id}")
        next_step = "no_videos"

    return {
        "goal_id": goal_id,
        "search_result": result,
        "status": "completed",
        "next_step": next_step
    }