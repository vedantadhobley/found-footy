"""Search Twitter for goal videos"""
import time
from datetime import datetime
from typing import Any, Dict, List
from urllib.parse import quote

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="search_twitter",
    description="Search Twitter for videos of a specific goal with retry logic",
    tags={"kind": "external", "service": "twitter"}
)
def search_twitter_op(context: OpExecutionContext, goal_id: str) -> Dict[str, Any]:
    """
    Search Twitter for tweets containing videos of the goal.
    
    Retry Strategy (Total: ~10 minutes):
    1. Wait 2 minutes initially for videos to be uploaded
    2. First search with player last name + team name
    3. If < 5 videos found, wait 3 minutes and retry
    4. If still < 5 videos, wait 4 minutes for final attempt
    5. Return whatever videos found (even if < 5)
    
    Time Filtering:
    - Only considers tweets posted AFTER the goal's created_at timestamp
    - This prevents getting old videos from previous matches
    
    Args:
        goal_id: The goal ID to search for
        
    Returns:
        Dict with goal_id and tweets list: {"goal_id": str, "tweets": List[Dict]}
    """
    context.log.info(f"🐦 Searching Twitter for goal {goal_id}")
    
    # Get goal doc to get created_at timestamp
    store = FootyMongoStore()
    goal_doc = store.goals_confirmed.find_one({"_id": goal_id})
    
    if not goal_doc:
        context.log.error(f"❌ Goal {goal_id} not found in goals_confirmed")
        return {"goal_id": goal_id, "tweets": []}
    
    created_at_str = goal_doc.get("created_at")
    player_name = goal_doc.get("player", {}).get("name", "Unknown")
    team_name = goal_doc.get("team", {}).get("name", "Unknown")
    time_data = goal_doc.get("time", {})
    minute = time_data.get("elapsed", "?")
    
    context.log.info(f"⚽ Goal: {team_name} - {player_name} {minute}'")
    
    # Extract last name for search (e.g., "Lionel Messi" -> "Messi")
    player_last_name = player_name.split()[-1] if player_name else "Unknown"
    
    # Build search query with last name only
    search_query = f"{player_last_name} {team_name}"
    video_search_query = f"{search_query} filter:videos"
    search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
    
    # Parse created_at for time filtering
    created_at = None
    if created_at_str:
        try:
            created_at = datetime.fromisoformat(created_at_str.replace('Z', '+00:00'))
        except:
            pass
    
    # Wait 2 minutes initially for videos to be uploaded to Twitter
    context.log.info("⏳ Waiting 2 minutes for videos to be uploaded to Twitter...")
    time.sleep(120)  # 2 minutes
    
    # Retry logic with time-based filtering
    def search_attempt(wait_time=0):
        if wait_time > 0:
            context.log.info(f"⏳ Waiting {wait_time} seconds for more videos to be uploaded...")
            time.sleep(wait_time)
        
        context.log.info(f"🔍 Searching: {search_url}")
        
        # Use Twitter session resource
        from src.resources.twitter_resource import get_twitter_resource
        
        try:
            twitter = get_twitter_resource()
            tweets = twitter.search_videos(search_query, max_results=10)
            context.log.info(f"📊 Found {len(tweets)} videos")
            return tweets
        except Exception as e:
            context.log.error(f"❌ Twitter search failed: {e}")
            return []
    
    # Initial search (after 2 min wait)
    tweets = search_attempt()
    
    # Retry if < 5 videos - wait 3 minutes
    if len(tweets) < 5:
        context.log.warning(f"⚠️  Only {len(tweets)} videos found, retrying in 3 minutes...")
        tweets = search_attempt(180)  # Wait 3 minutes
    
    # Final retry if still < 5 - wait 4 minutes
    if len(tweets) < 5:
        context.log.warning(f"⚠️  Still only {len(tweets)} videos, final retry in 4 minutes...")
        tweets = search_attempt(240)  # Wait 4 minutes
    
    context.log.info(f"✅ Final search complete: {len(tweets)} videos found")
    
    # Filter tweets by created_at if available
    if created_at and tweets:
        filtered_tweets = []
        for tweet in tweets:
            tweet_time_str = tweet.get("timestamp")
            if tweet_time_str:
                try:
                    tweet_time = datetime.fromisoformat(tweet_time_str.replace('Z', '+00:00'))
                    if tweet_time >= created_at:
                        filtered_tweets.append(tweet)
                except:
                    # If can't parse, include it anyway
                    filtered_tweets.append(tweet)
            else:
                filtered_tweets.append(tweet)
        
        removed = len(tweets) - len(filtered_tweets)
        if removed > 0:
            context.log.info(f"🗑️  Filtered out {removed} tweets posted before goal")
        tweets = filtered_tweets
    
    # Add goal_id to each tweet for downstream ops
    for tweet in tweets:
        tweet["goal_id"] = goal_id
    
    # Return dict with goal_id and tweets for downstream ops
    return {
        "goal_id": goal_id,
        "tweets": tweets
    }
