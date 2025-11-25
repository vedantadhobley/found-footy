"""Search Twitter for goal videos"""
import time
from datetime import datetime
from typing import Any, Dict, List
from urllib.parse import quote

from dagster import Config, OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


class SearchTwitterConfig(Config):
    """Configuration for search_twitter_op"""
    event_id: str


@op(
    name="search_twitter",
    description="Search Twitter for videos of a specific goal with retry logic",
    tags={"kind": "external", "service": "twitter"}
)
def search_twitter_op(context: OpExecutionContext, config: SearchTwitterConfig) -> Dict[str, Any]:
    """
    Search Twitter for tweets containing videos of the event.
    
    Retry Strategy (Total: ~10 minutes):
    1. Wait 2 minutes initially for videos to be uploaded
    2. First search with player last name + team name
    3. If < 5 videos found, wait 3 minutes and retry
    4. If still < 5 videos, wait 4 minutes for final attempt
    5. Return whatever videos found (even if < 5)
    
    Time Filtering:
    - Only considers tweets posted AFTER the event's confirmed_at timestamp
    - This prevents getting old videos from previous matches
    
    Args:
        event_id: The event ID to search for
        
    Returns:
        Dict with event_id and tweets list: {"event_id": str, "tweets": List[Dict]}
    """
    event_id = config.event_id
    context.log.info(f"🐦 Searching Twitter for event {event_id}")
    
    # Get event from events_confirmed
    store = FootyMongoStore()
    event_doc = store.get_confirmed_event(event_id)
    
    if not event_doc:
        context.log.error(f"❌ Event {event_id} not found in events_confirmed")
        return {"event_id": event_id, "tweets": []}
    
    # Get fixtures_tracked for additional context
    fixture_id = event_doc.get("fixture_id")
    tracked = store.get_tracked_fixture(fixture_id)
    
    # Get twitter_search from fixtures_tracked (prebuilt search string)
    if tracked and event_id in tracked.get("tracked_events", {}):
        twitter_search = tracked["tracked_events"][event_id].get("twitter_search", "")
    else:
        # Fallback: build from event_doc
        player_name = event_doc.get("event_data", {}).get("player", {}).get("name", "Unknown")
        team_name = event_doc.get("event_data", {}).get("team", {}).get("name", "Unknown")
        player_last_name = player_name.split()[-1] if player_name else "Unknown"
        twitter_search = f"{player_last_name} {team_name}"
    
    confirmed_at_str = event_doc.get("confirmed_at")
    time_data = event_doc.get("event_data", {}).get("time", {})
    minute = time_data.get("elapsed", "?")
    
    context.log.info(f"⚽ Event: {twitter_search} {minute}'")
    
    # Use prebuilt twitter_search string
    search_query = twitter_search
    video_search_query = f"{search_query} filter:videos"
    search_url = f"https://twitter.com/search?q={quote(video_search_query)}&src=typed_query&f=live"
    
    # Parse confirmed_at for time filtering
    confirmed_at = None
    if confirmed_at_str:
        try:
            confirmed_at = datetime.fromisoformat(confirmed_at_str.replace('Z', '+00:00'))
        except:
            pass
    
    # Update fixtures_tracked to mark twitter as started
    store.update_tracked_event_twitter(fixture_id, event_id, twitter_started=True)
    
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
    
    # Filter tweets by confirmed_at if available
    if confirmed_at and tweets:
        filtered_tweets = []
        for tweet in tweets:
            tweet_time_str = tweet.get("timestamp")
            if tweet_time_str:
                try:
                    tweet_time = datetime.fromisoformat(tweet_time_str.replace('Z', '+00:00'))
                    if tweet_time >= confirmed_at:
                        filtered_tweets.append(tweet)
                except:
                    # If can't parse, include it anyway
                    filtered_tweets.append(tweet)
            else:
                filtered_tweets.append(tweet)
        
        removed = len(tweets) - len(filtered_tweets)
        if removed > 0:
            context.log.info(f"🗑️  Filtered out {removed} tweets posted before event")
        tweets = filtered_tweets
    
    # Add event_id to each tweet for downstream ops
    for tweet in tweets:
        tweet["event_id"] = event_id
    
    # Update fixtures_tracked to mark twitter as complete
    store.update_tracked_event_twitter(fixture_id, event_id, twitter_started=False)
    context.log.info(f"✅ Updated fixtures_tracked twitter_complete for {event_id}")
    
    # Return dict with event_id and tweets for downstream ops
    return {
        "event_id": event_id,
        "fixture_id": fixture_id,
        "tweets": tweets
    }
