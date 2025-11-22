"""Twitter scraping op - searches Twitter for a single goal's videos"""

from dagster import op, Config, Backoff, Jitter, RetryPolicy
from typing import Dict, Any
from pymongo import MongoClient
from bson import ObjectId
from datetime import datetime


class MongoConfig(Config):
    """MongoDB connection configuration."""
    mongo_uri: str = "mongodb://localhost:27017"
    db_name: str = "found_footy"


@op(
    name="scrape_twitter",
    description="Search Twitter for videos of a single goal",
    retry_policy=RetryPolicy(
        max_retries=3,
        delay=10,  # 10 seconds between retries
        backoff=Backoff.EXPONENTIAL,
        jitter=Jitter.PLUS_MINUS
    )
)
def scrape_twitter_op(context, config: MongoConfig, process_result: Dict) -> Dict[str, Any]:
    """
    Search Twitter for videos of ONE goal - matches twitter_flow.py.
    
    Uses cookie-based authentication (more reliable after Twitter login changes).
    Retries up to 3 times on failure (Twitter API can be flaky).
    Uses exponential backoff: 10s, 20s, 40s.
    """
    
    goal_id = process_result["goal_id"]
    player = process_result["player"]
    minute = process_result["minute"]
    home_team = process_result["home_team"]
    away_team = process_result["away_team"]
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Build search query (player last name + teams)
    player_last_name = player.split()[-1] if " " in player else player
    query = f"{player_last_name} {home_team} {away_team}"
    
    context.log.info(f"🔍 Searching Twitter for: {query}")
    
    try:
        # Use Twitter session service (cookie-based auth)
        import requests
        import os
        
        session_url = os.getenv('TWITTER_SESSION_URL', 'http://twitter-session:8888')
        
        response = requests.post(
            f"{session_url}/search",
            json={"search_query": query, "max_results": 10},
            timeout=60
        )
        
        if response.status_code != 200:
            context.log.error(f"❌ Twitter service returned {response.status_code}")
            context.log.error("   Make sure twitter-session container is running with valid cookies")
            raise Exception("Twitter service unavailable or not authenticated")
        
        data = response.json()
        videos = data.get("videos", [])
        
        context.log.info(f"Found {len(videos)} videos")
        
        # Store video metadata in MongoDB
        video_ids = []
        for video in videos:
            video_doc = {
                "goal_id": ObjectId(goal_id),
                "tweet_url": video["tweet_url"],
                "video_url": video["video_url"],
                "author": video["author"],
                "text": video["text"],
                "created_at": video["created_at"],
                "download_status": "pending",
                "upload_status": "pending",
                "discovered_at": datetime.utcnow()
            }
            
            # Upsert to avoid duplicates
            result = db.videos.update_one(
                {"goal_id": ObjectId(goal_id), "tweet_url": video["tweet_url"]},
                {"$set": video_doc},
                upsert=True
            )
            
            if result.upserted_id:
                video_ids.append(str(result.upserted_id))
        
        # Update goal status
        db.goals.update_one(
            {"_id": ObjectId(goal_id)},
            {"$set": {"processing_status.twitter_scraped": True}}
        )
        
        client.close()
        
        context.log.info(f"✅ Scraped {len(videos)} videos for goal {goal_id}")
        
        return {
            "goal_id": goal_id,
            "player": player,
            "minute": minute,
            "videos_found": len(videos),
            "video_ids": video_ids
        }
    
    except Exception as e:
        client.close()
        context.log.error(f"❌ Twitter search failed: {e}")
        raise  # Re-raise to trigger retry policy
