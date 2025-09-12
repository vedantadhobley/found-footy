import json
from prefect import flow, task, get_run_logger
from prefect.deployments import run_deployment
from found_footy.storage.mongo_store import FootyMongoStore
from datetime import datetime, timezone
from typing import Optional, List, Dict, Any
import snscrape.modules.twitter as sntwitter  # ✅ USE: Python API

store = FootyMongoStore()

@task(name="twitter-search-task")
def twitter_search_task(goal_id: str) -> Dict[str, Any]:
    """Search Twitter for goal videos using snscrape Python API"""
    logger = get_run_logger()
    
    logger.info(f"🔍 Twitter search for goal videos: {goal_id}")
    
    try:
        goal_doc = store.goals_pending.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")
            return {"status": "not_found", "goal_id": goal_id}
        
        logger.info(f"🚨 GOAL FOUND: {goal_doc['team_name']} - {goal_doc['player_name']} ({goal_doc['minute']}')")
        
        # Extract search terms
        player_name = goal_doc['player_name']
        team_name = goal_doc['team_name']
        minute = goal_doc['minute']
        
        # Extract last name from full player name
        player_last_name = player_name.split()[-1] if ' ' in player_name else player_name
        
        # ✅ CLEAN SEARCH TERMS: Just "LastName TeamName"
        primary_search = f"{player_last_name} {team_name}"
        
        logger.info(f"🐦 SNSCRAPE SEARCH: '{primary_search}'")
        
        # ✅ USE SNSCRAPE PYTHON API
        found_videos = []
        
        try:
            # Create search query with media filter
            search_query = f"{primary_search} filter:media"
            
            logger.info(f"🔍 Using snscrape Python API for: {search_query}")
            
            # Use snscrape Python API
            tweets_with_videos = []
            tweet_count = 0
            max_results = 5
            
            for tweet in sntwitter.TwitterSearchScraper(search_query).get_items():
                if tweet_count >= max_results:
                    break
                
                # ✅ CHECK IF TWEET HAS VIDEO MEDIA
                if hasattr(tweet, 'media') and tweet.media:
                    has_video = any(
                        hasattr(media, 'type') and 'video' in str(media.type).lower() 
                        for media in tweet.media
                    )
                    
                    if has_video:
                        tweets_with_videos.append(tweet)
                        tweet_count += 1
                        logger.info(f"✅ Found video tweet: {tweet.url}")
            
            logger.info(f"✅ snscrape found {len(tweets_with_videos)} tweets with video for '{primary_search}'")
            
            # ✅ STORE TWEET URLs FOR DOWNLOAD FLOW
            for j, tweet in enumerate(tweets_with_videos):
                video_info = {
                    "search_term": primary_search,
                    "tweet_url": tweet.url,
                    "tweet_id": str(tweet.id),
                    "tweet_text": tweet.rawContent,
                    "username": tweet.user.username,
                    "timestamp": tweet.date.isoformat() if tweet.date else datetime.now(timezone.utc).isoformat(),
                    "discovered_at": datetime.now(timezone.utc).isoformat(),
                    "search_index": 0,
                    "video_index": j,
                    "source": "snscrape_python",
                    "requires_ytdlp": True  # ✅ Flag for download flow
                }
                
                found_videos.append(video_info)
                logger.info(f"✅ Found tweet: {tweet.url}")
            
        except Exception as e:
            logger.error(f"❌ Error running snscrape Python API: {e}")
            raise
        
        # Update goal document with discovered tweet URLs
        goal_doc["twitter_search_completed"] = True
        goal_doc["discovered_videos"] = found_videos
        goal_doc["search_terms_used"] = [primary_search]
        goal_doc["twitter_search_at"] = datetime.now(timezone.utc)
        goal_doc["search_method"] = "snscrape_python"
        goal_doc["status"] = "videos_discovered"
        
        # Update in database (keep in pending for download flow)
        store.goals_pending.replace_one({"_id": goal_id}, goal_doc)
        
        logger.info(f"✅ Twitter search completed - found {len(found_videos)} tweet URLs for '{primary_search}'")
        
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
            "search_method": "snscrape_python"
        }
        
    except Exception as e:
        logger.error(f"❌ Error in Twitter search for goal {goal_id}: {e}")
        return {"status": "error", "goal_id": goal_id, "error": str(e)}

@flow(
    name="twitter-flow"
)
def twitter_flow(goal_id: Optional[str] = None):
    """Twitter video search flow using snscrape Python API"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}
    
    logger.info(f"🔍 Starting snscrape Python API search for goal: {goal_id}")
    
    # Search Twitter for tweet URLs using snscrape Python API
    search_result = twitter_search_task(goal_id)
    
    if search_result["status"] == "success":
        video_count = search_result["video_count"]
        search_term = search_result["search_terms"][0]
        logger.info(f"✅ Twitter search completed for '{search_term}' - found {video_count} tweet URLs")
        
        if video_count > 0:
            # ✅ TRIGGER DOWNLOAD FLOW
            from found_footy.flows.flow_naming import get_download_flow_name
            download_flow_name = get_download_flow_name(goal_id)
            
            run_deployment(
                name="download-flow/download-flow",
                parameters={"goal_id": goal_id},
                flow_run_name=download_flow_name
            )
            
            logger.info(f"📥 Triggered download flow: {download_flow_name}")
        else:
            logger.warning(f"⚠️ No tweet URLs found for '{search_term}' - skipping download")
    else:
        logger.error(f"❌ Twitter search failed: {search_result.get('error')}")
    
    return {
        "goal_id": goal_id,
        "search_result": search_result,
        "status": "completed",
        "search_method": "snscrape_python",
        "next_step": "download_flow" if search_result["status"] == "success" else "failed"
    }