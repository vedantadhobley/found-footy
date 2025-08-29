from datetime import datetime
from prefect import flow, task, get_run_logger
import time

@task(retries=10, retry_delay_seconds=1800)  # Retry 10 times, every 30 minutes
def monitor_youtube(team1: str, team2: str, match_date: str):
    """Monitor YouTube for match highlights - simplified for clarity"""
    logger = get_run_logger()
    
    logger.info(f"🔍 YouTube monitoring cycle for {team1} vs {team2} on {match_date}")
    
    # Simulate YouTube search (replace with actual implementation later)
    logger.info(f"📺 Searching for highlights: {team1} vs {team2}")
    
    # Add small delay to simulate search
    time.sleep(2)
    
    # For now, just log that we're monitoring (no video spam)
    logger.info(f"⏳ No highlights found yet for {team1} vs {team2}")
    
    # Raise exception to trigger retry cycle
    raise Exception(f"No highlights found yet - will retry in 30 minutes")

@flow(name="youtube-flow")
def youtube_flow(team1: str = "Barcelona", team2: str = "Liverpool", match_date: str = None, trace_id: str = None):
    """Enhanced YouTube flow with tracing"""
    logger = get_run_logger()
    
    if not match_date:
        match_date = datetime.now().strftime("%Y-%m-%d")
    
    # ✅ Enhanced logging for traceability
    logger.info(f"🎬 Starting YouTube monitoring for {team1} vs {team2}")
    logger.info(f"🗓️ Match date: {match_date}")
    if trace_id:
        logger.info(f"🔗 Trace ID: {trace_id} (linked to fixture completion)")
    logger.info("📺 Will check every 30 minutes for highlights (max 5 hours)")
    
    try:
        videos = monitor_youtube(team1, team2, match_date)
        logger.info(f"✅ Found highlights for {team1} vs {team2}!")
        
        return {
            "status": "success",
            "match": f"{team1} vs {team2}",
            "date": match_date,
            "trace_id": trace_id,
            "videos_found": len(videos) if videos else 0,
            "monitoring_completed_at": datetime.now().isoformat()
        }
        
    except Exception as e:
        logger.error(f"❌ YouTube monitoring failed: {e}")
        return {
            "status": "failed",
            "match": f"{team1} vs {team2}",
            "date": match_date,
            "trace_id": trace_id,
            "error": str(e),
            "monitoring_completed_at": datetime.now().isoformat()
        }

if __name__ == "__main__":
    # Test the flow locally
    result = youtube_flow("Manchester United", "Liverpool", "2024-08-27")
    print(f"\n✅ Flow completed: {result}")