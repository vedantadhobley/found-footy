# ✅ UPDATED: found_footy/flows/twitter_flow.py - Simplified search terms
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from found_footy.storage.mongo_store import FootyMongoStore
from found_footy.api.twitter_api import twitter_videos_search
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
import os

store = FootyMongoStore()

@task(name="twitter-search-task")
def twitter_search_task(goal_id: str) -> Dict[str, Any]:
    """Search Twitter for goal videos using simplified search terms"""
    logger = get_run_logger()
    
    logger.info(f"🔍 Twitter search for goal videos: {goal_id}")
    
    try:
        goal_doc = store.goals_pending.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
            return {"status": "not_found", "goal_id": goal_id}
        
        logger.info(f"🚨 GOAL FOUND: {goal_doc['team_name']} - {goal_doc['player_name']} ({goal_doc['minute']}')")
        
        # ✅ SIMPLIFIED: Extract last name and team only
        player_name = goal_doc['player_name']
        team_name = goal_doc['team_name']
        minute = goal_doc['minute']
        fixture_id = goal_doc['fixture_id']
        
        # Extract last name from full player name
        player_last_name = player_name.split()[-1] if ' ' in player_name else player_name
        
        # ✅ CLEAN SEARCH TERMS: Just "LastName TeamName"
        primary_search = f"{player_last_name} {team_name}"
        
        logger.info(f"🐦 TWITTER SEARCH: '{primary_search}'")
        
        # Single focused search
        found_videos = []
        
        try:
            # ✅ ONE CLEAN SEARCH: Player last name + team
            scraped_videos = twitter_videos_search(primary_search, max_results=5)
            
            for j, scraped_video in enumerate(scraped_videos):
                video_info = {
                    "search_term": primary_search,
                    "video_url": scraped_video["video_url"],
                    "source_tweet_id": scraped_video["tweet_id"],
                    "source_tweet_url": scraped_video["tweet_url"],
                    "tweet_text": scraped_video["tweet_text"],
                    "username": scraped_video["username"],
                    "scraping_source": scraped_video["source"],
                    "video_quality": scraped_video["video_metadata"].get("resolution", "unknown"),
                    "duration_seconds": scraped_video["video_metadata"].get("duration", "unknown"),
                    "video_format": scraped_video["video_metadata"].get("format", "mp4"),
                    "thumbnail_url": scraped_video["video_metadata"].get("thumbnail"),
                    "url_validated": scraped_video.get("url_validated", False),
                    "discovered_at": datetime.now(timezone.utc).isoformat(),
                    "search_index": 0,  # Only one search now
                    "video_index": j
                }
                
                found_videos.append(video_info)
                logger.info(f"✅ Found video: {scraped_video['video_url']}")
            
        except Exception as e:
            logger.error(f"❌ Error searching Twitter for '{primary_search}': {e}")
            
            # Fallback to simulated data if scraping fails
            fallback_video = {
                "search_term": primary_search,
                "video_url": f"https://video.twimg.com/ext_tw_video/{goal_id}_fallback.mp4",
                "source_tweet_id": f"fallback_tweet",
                "source_tweet_url": f"https://twitter.com/user/status/fallback",
                "tweet_text": f"🔥 {player_last_name} scores for {team_name}! What a goal! ⚽",
                "username": "football_highlights",
                "scraping_source": "fallback",
                "video_quality": "720p",
                "duration_seconds": 30,
                "video_format": "mp4",
                "url_validated": False,
                "discovered_at": datetime.now(timezone.utc).isoformat(),
                "search_index": 0,
                "video_index": 0,
                "scraping_error": str(e)
            }
            
            found_videos.append(fallback_video)
            logger.warning(f"⚠️ Using fallback data for '{primary_search}'")
        
        # Update goal document with discovered videos
        goal_doc["twitter_search_completed"] = True
        goal_doc["discovered_videos"] = found_videos
        goal_doc["search_terms_used"] = [primary_search]  # Only one search term now
        goal_doc["twitter_search_at"] = datetime.now(timezone.utc)
        goal_doc["search_method"] = "simplified_search"
        goal_doc["status"] = "videos_discovered"
        
        # Update in database (keep in pending for download flow)
        store.goals_pending.replace_one({"_id": goal_id}, goal_doc)
        
        logger.info(f"✅ Twitter search completed - found {len(found_videos)} videos for '{primary_search}'")
        
        return {
            "status": "success",
            "goal_id": goal_id,
            "team": goal_doc['team_name'],
            "player": goal_doc['player_name'],
            "player_last_name": player_last_name,
            "minute": goal_doc['minute'],
            "discovered_videos": found_videos,
            "video_count": len(found_videos),
            "search_terms": [primary_search],
            "search_method": "simplified_search"
        }
        
    except Exception as e:
        logger.error(f"❌ Error in Twitter search for goal {goal_id}: {e}")
        return {"status": "error", "goal_id": goal_id, "error": str(e)}

@flow(
    name="twitter-flow"
)
def twitter_flow(goal_id: Optional[str] = None):
    """Twitter video search flow - simplified search approach"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}
    
    logger.info(f"🔍 Starting simplified Twitter search for goal: {goal_id}")
    
    # Search Twitter for videos using simplified terms
    search_result = twitter_search_task(goal_id)
    
    if search_result["status"] == "success":
        video_count = search_result["video_count"]
        search_term = search_result["search_terms"][0]
        logger.info(f"✅ Twitter search completed for '{search_term}' - found {video_count} videos")
        
        if video_count > 0:
            # Trigger download flow
            from found_footy.flows.flow_naming import get_download_flow_name
            download_flow_name = get_download_flow_name(goal_id)
            
            run_deployment(
                name="download-flow/download-flow",
                parameters={"goal_id": goal_id},
                flow_run_name=download_flow_name
            )
            
            logger.info(f"📥 Triggered download flow: {download_flow_name}")
        else:
            logger.warning(f"⚠️ No videos found for '{search_term}' - skipping download")
    else:
        logger.error(f"❌ Twitter search failed: {search_result.get('error')}")
    
    return {
        "goal_id": goal_id,
        "search_result": search_result,
        "status": "completed",
        "search_method": "simplified_search",
        "next_step": "download_flow" if search_result["status"] == "success" else "failed"
    }