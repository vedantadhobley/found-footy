# ✅ UPDATED: found_footy/flows/twitter_flow.py
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment  # ✅ ADD: Missing import
from found_footy.storage.mongo_store import FootyMongoStore
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
import os

store = FootyMongoStore()

@task(name="twitter-search-task")
def twitter_search_task(goal_id: str) -> Dict[str, Any]:
    """Search Twitter for goal videos and collect video URLs"""
    logger = get_run_logger()
    
    logger.info(f"🔍 Searching Twitter for goal videos: {goal_id}")
    
    try:
        goal_doc = store.goals_pending.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
            return {"status": "not_found", "goal_id": goal_id}
        
        logger.info(f"🚨 GOAL FOUND: {goal_doc['team_name']} - {goal_doc['player_name']} ({goal_doc['minute']}')")
        
        # Build search query for Twitter
        player_name = goal_doc['player_name']
        team_name = goal_doc['team_name']
        minute = goal_doc['minute']
        fixture_id = goal_doc['fixture_id']
        
        # Create search terms for the goal
        search_terms = [
            f"{player_name} goal",
            f"{team_name} goal {minute}",
            f"{player_name} {team_name}",
            f"goal {minute}'"
        ]
        
        logger.info(f"🐦 TWITTER SEARCH: Looking for videos with terms: {search_terms}")
        
        # Search Twitter and collect video URLs
        found_videos = []
        
        for i, search_term in enumerate(search_terms[:3]):  # Limit to 3 searches
            logger.info(f"🔍 Searching Twitter for: '{search_term}'")
            
            # TODO: Implement actual Twitter API search
            # For now, simulate finding video URLs
            
            # In real implementation:
            # 1. Use Twitter API v2 to search for tweets with search_term
            # 2. Filter tweets that contain video/media
            # 3. Extract video URLs from tweet media entities
            # 4. Validate URLs are accessible
            
            # Simulate finding video URLs
            video_urls = [
                f"https://video.twimg.com/ext_tw_video/{goal_id}_{i}_1.mp4",
                f"https://video.twimg.com/ext_tw_video/{goal_id}_{i}_2.mp4"
            ]
            
            for j, video_url in enumerate(video_urls):
                found_videos.append({
                    "search_term": search_term,
                    "video_url": video_url,
                    "source_tweet_id": f"simulated_tweet_{i}_{j}",
                    "source_tweet_url": f"https://twitter.com/user/status/{i}{j}123456789",
                    "video_quality": "720p",  # Would be detected from actual API
                    "duration_seconds": 30,   # Would be detected from actual API
                    "discovered_at": datetime.now(timezone.utc).isoformat(),
                    "search_index": i,
                    "video_index": j
                })
                
                logger.info(f"✅ Found video: {video_url}")
        
        # Update goal document with discovered videos
        goal_doc["twitter_search_completed"] = True
        goal_doc["discovered_videos"] = found_videos
        goal_doc["search_terms_used"] = search_terms
        goal_doc["twitter_search_at"] = datetime.now(timezone.utc)
        goal_doc["status"] = "videos_discovered"
        
        # Update in database (keep in pending for download flow)
        store.goals_pending.replace_one({"_id": goal_id}, goal_doc)
        
        logger.info(f"✅ Twitter search completed - found {len(found_videos)} videos for {goal_id}")
        
        return {
            "status": "success",
            "goal_id": goal_id,
            "team": goal_doc['team_name'],
            "player": goal_doc['player_name'],
            "minute": goal_doc['minute'],
            "discovered_videos": found_videos,
            "video_count": len(found_videos),
            "search_terms": search_terms
        }
        
    except Exception as e:
        logger.error(f"❌ Error searching Twitter for goal {goal_id}: {e}")
        return {"status": "error", "goal_id": goal_id, "error": str(e)}

@flow(
    name="twitter-flow"
)
def twitter_flow(goal_id: Optional[str] = None):
    """Twitter video search flow - discovers goal videos and triggers download"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}
    
    logger.info(f"🔍 Starting Twitter search for goal: {goal_id}")
    
    # Search Twitter for videos
    search_result = twitter_search_task(goal_id)
    
    if search_result["status"] == "success":
        video_count = search_result["video_count"]
        logger.info(f"✅ Twitter search completed - found {video_count} videos")
        
        if video_count > 0:
            # Trigger download flow
            from found_footy.flows.flow_naming import get_download_flow_name
            download_flow_name = get_download_flow_name(goal_id)
            
            run_deployment(  # ✅ NOW THIS IMPORT EXISTS
                name="download-flow/download-flow",
                parameters={"goal_id": goal_id},
                flow_run_name=download_flow_name
            )
            
            logger.info(f"📥 Triggered download flow: {download_flow_name}")
        else:
            logger.warning(f"⚠️ No videos found for goal {goal_id} - skipping download")
    else:
        logger.error(f"❌ Twitter search failed: {search_result.get('error')}")
    
    return {
        "goal_id": goal_id,
        "search_result": search_result,
        "status": "completed",
        "next_step": "download_flow" if search_result["status"] == "success" else "failed"
    }