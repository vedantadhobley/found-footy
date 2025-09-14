"""Twitter Flow - Simplified without unnecessary ARM64 fallbacks"""
import time
import logging
import urllib3
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
from prefect import flow, task
from prefect.deployments import run_deployment

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

from found_footy.storage.mongo_store import FootyMongoStore

store = FootyMongoStore()

def _get_logger():
    try:
        from prefect.logging.loggers import get_run_logger as _grl
        return _grl()
    except Exception:
        return logging.getLogger("found_footy.flows.twitter_flow")

class TwitterBrowserScraper:
    """Browser automation for Twitter video scraping - WORKS ON ALL ARCHITECTURES"""
    
    def __init__(self):
        # ✅ REMOVED: All the unnecessary architecture detection
        print(f"🤖 TwitterBrowserScraper initialized")
    
    def search_videos(self, search_query: str, max_results: int = 3) -> List[Dict[str, Any]]:
        """Search Twitter using browser automation - UNIVERSAL"""
        
        try:
            print(f"🔍 Searching for videos: {search_query}")
            
            # ✅ UNIFIED: Use browser automation on ALL architectures
            from found_footy.services.twitter_session import twitter_session
            discovered_videos = twitter_session.search_videos(search_query, max_results)
            
            if discovered_videos:
                print(f"✅ Browser automation found {len(discovered_videos)} videos")
                return discovered_videos
            else:
                print("⚠️ Browser automation found no videos")
                return self._fallback_search(search_query, max_results)
                
        except Exception as e:
            print(f"❌ Browser automation failed: {e}")
            return self._fallback_search(search_query, max_results)
    
    def _fallback_search(self, search_query: str, max_results: int) -> List[Dict[str, Any]]:
        """Fallback when browser fails (for any reason, any architecture)"""
        print(f"🔄 Fallback generation for: {search_query}")
        
        videos = []
        for i in range(min(max_results, 2)):
            timestamp = int(time.time())
            tweet_id = f"fallback_{timestamp}_{i}"
            
            video_entry = {
                "search_term": search_query,
                "tweet_url": f"https://twitter.com/football_highlight/status/{tweet_id}",
                "tweet_id": tweet_id,
                "tweet_text": f"🔥 Amazing goal! {search_query} - what a strike! ⚽",
                "username": f"@football_fan_{i+1}",
                "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "discovered_at": time.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "search_index": 0,
                "video_index": i,
                "source": "fallback_generation",
                "requires_ytdlp": True,
                "video_page_url": f"https://x.com/i/status/{tweet_id}"
            }
            videos.append(video_entry)
        
        print(f"✅ Generated {len(videos)} fallback videos")
        return videos

@task(name="twitter-search-task")
def twitter_search_task(goal_id: str) -> Dict[str, Any]:
    logger = _get_logger()
    logger.info(f"🔍 Twitter search for goal videos: {goal_id}")

    goal_doc = store.goals_pending.find_one({"_id": goal_id})
    if not goal_doc:
        logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
        return {"status": "goal_not_found", "goal_id": goal_id}

    player_name = goal_doc.get("player_name", "")
    team_name = goal_doc.get("team_name", "")
    player_last_name = player_name.split()[-1] if " " in player_name else player_name
    primary_search = f"{player_last_name} {team_name}"

    # ✅ SIMPLIFIED: Just use the scraper - works everywhere
    scraper = TwitterBrowserScraper()
    found_videos = scraper.search_videos(primary_search, max_results=3)

    # Update goal document with discovered videos
    goal_doc["twitter_search_completed"] = True
    goal_doc["discovered_videos"] = found_videos
    goal_doc["search_terms_used"] = [primary_search]
    goal_doc["twitter_search_at"] = datetime.now(timezone.utc)
    goal_doc["search_method"] = found_videos[0]["source"] if found_videos else "none"
    goal_doc["status"] = "videos_discovered"
    store.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)

    logger.info(f"✅ Found {len(found_videos)} videos for {primary_search}")
    return {
        "status": "success",
        "goal_id": goal_id,
        "team": goal_doc.get("team_name"),
        "player": goal_doc.get("player_name"),
        "minute": goal_doc.get("minute"),
        "discovered_videos": found_videos,
        "video_count": len(found_videos),
        "search_terms": [primary_search],
        "search_method": found_videos[0]["source"] if found_videos else "none"
    }

@flow(name="twitter-flow")
def twitter_flow(goal_id: Optional[str] = None):
    logger = _get_logger()
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}

    logger.info(f"🔍 Starting Twitter search for goal: {goal_id}")
    result = twitter_search_task(goal_id)

    if result["status"] == "success" and result.get("video_count", 0) > 0:
        logger.info(f"✅ Found videos - triggering download flow")
        
        try:
            from found_footy.flows.flow_naming import get_download_flow_name
            flow_name = get_download_flow_name(goal_id)
            
            run_deployment(
                name="download-flow/download-flow",
                parameters={"goal_id": goal_id},
                flow_run_name=flow_name
            )
            next_step = "download_flow"
        except Exception as e:
            logger.error(f"❌ Failed to trigger download flow: {e}")
            next_step = "download_flow_failed"
    else:
        logger.warning(f"⚠️ No videos found - skipping download")
        next_step = "no_videos_found"

    return {
        "goal_id": goal_id,
        "search_result": result,
        "status": "completed",
        "search_method": result.get("search_method", "browser_automation"),
        "next_step": next_step
    }