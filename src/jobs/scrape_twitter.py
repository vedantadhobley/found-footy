"""Twitter scraping job - finds goal videos on Twitter

Migrated from found_footy/flows/twitter_flow.py
"""

import os
import logging
import requests
from typing import Dict, Any

from dagster import job, op, OpExecutionContext, Config
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


class ScrapeTwitterConfig(Config):
    """Configuration for Twitter scraping"""
    goal_id: str


@op(
    name="search_twitter_videos",
    description="Search Twitter for goal videos and store URLs"
)
def search_twitter_videos_op(
    context: OpExecutionContext,
    config: ScrapeTwitterConfig
) -> Dict[str, Any]:
    """
    Search Twitter for videos:
    1. Get goal details from MongoDB
    2. Build search query
    3. Call Twitter session service
    4. Store discovered video URLs
    """
    goal_id = config.goal_id
    context.log.info(f"🔍 Twitter search for goal: {goal_id}")
    
    store = FootyMongoStore()
    goal_doc = store.goals.find_one({"_id": goal_id})
    
    if not goal_doc:
        context.log.warning(f"Goal {goal_id} not found")
        return {"status": "goal_not_found", "goal_id": goal_id}
    
    # Extract player and team info
    player_name = goal_doc.get("player", {}).get("name", "")
    team_name = goal_doc.get("team", {}).get("name", "")
    
    if not player_name or not team_name:
        context.log.warning("Missing player/team data")
        store.update_goal_processing_status(goal_id, "videos_discovered", discovered_videos=[])
        return {"status": "missing_data", "goal_id": goal_id, "video_count": 0}
    
    # Build search query
    player_last_name = player_name.split()[-1] if " " in player_name else player_name
    search_query = f"{player_last_name} {team_name}"
    
    context.log.info(f"🔍 Searching: '{search_query}'")
    
    # Call Twitter session service
    session_url = os.getenv('TWITTER_SESSION_URL', 'http://twitter-session:8888')
    try:
        response = requests.post(
            f"{session_url}/search",
            json={"search_query": search_query, "max_results": 5},
            timeout=60
        )
        
        if response.status_code == 200:
            data = response.json()
            found_videos = data.get("videos", [])
        else:
            context.log.warning(f"Twitter API returned status {response.status_code}")
            found_videos = []
    except Exception as e:
        context.log.error(f"Twitter API error: {e}")
        found_videos = []
    
    # Update goal processing status
    store.update_goal_processing_status(
        goal_id,
        "videos_discovered",
        discovered_videos=found_videos,
        twitter_search_completed=True
    )
    
    context.log.info(f"✅ Found {len(found_videos)} videos for '{search_query}'")
    
    return {
        "status": "success",
        "goal_id": goal_id,
        "search_query": search_query,
        "video_count": len(found_videos),
        "videos": found_videos
    }


@job(
    name="scrape_twitter",
    description="Search Twitter for goal videos"
)
def scrape_twitter_job():
    """Twitter scraping workflow"""
    search_twitter_videos_op()
