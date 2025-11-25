"""Extract video URLs from tweets"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op


@op(
    name="extract_videos",
    description="Extract video URLs from tweet objects",
    tags={"kind": "logic", "purpose": "extraction"}
)
def extract_videos_op(
    context: OpExecutionContext,
    search_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Extract video URLs from tweets.
    
    Args:
        search_result: Dict with event_id and tweets from search_twitter_op
        
    Returns:
        Dict with event_id and video metadata list
    """
    event_id = search_result.get("event_id", "unknown")
    fixture_id = search_result.get("fixture_id")
    tweets = search_result.get("tweets", [])
    
    if not tweets:
        context.log.warning(f"⚠️  No tweets to extract videos from for event {event_id}")
        return {"event_id": event_id, "fixture_id": fixture_id, "videos": []}
    
    context.log.info(f"📹 Extracting videos from {len(tweets)} tweets for event {event_id}")
    
    videos = []
    
    for tweet in tweets:
        try:
            tweet_id = tweet.get('tweet_id', 'unknown')
            tweet_url = tweet.get('tweet_url', '')
            video_page_url = tweet.get('video_page_url', tweet_url)
            
            # For Twitter videos, we use the tweet URL which yt-dlp can handle
            # yt-dlp will extract the actual video URL
            
            # Extract video metadata
            video_metadata = {
                "event_id": event_id,  # Pass through event_id from tweet metadata
                "tweet_id": tweet_id,
                "tweet_url": tweet_url,
                "video_url": video_page_url,  # yt-dlp will handle the tweet URL
                "author": tweet.get('username', 'Unknown'),
                "text": tweet.get('tweet_text', ''),
                "timestamp": tweet.get('timestamp'),
                "search_term": tweet.get('search_term', ''),
                "source": tweet.get('source', 'browser_automation')
            }
            
            videos.append(video_metadata)
            context.log.info(f"   ✅ Extracted video from tweet {tweet_id}")
            
        except Exception as e:
            context.log.error(f"❌ Error extracting video from tweet: {e}")
            continue
    
    context.log.info(f"✅ Extracted {len(videos)} video URLs for event {event_id}")
    
    return {"event_id": event_id, "fixture_id": fixture_id, "videos": videos}
