# ✅ UPDATED: found_footy/flows/twitter_flow.py
from prefect import flow, task, get_run_logger
from found_footy.storage.mongo_store import FootyMongoStore
from datetime import datetime, timezone
from typing import Optional

store = FootyMongoStore()

@task(name="twitter-process-goal-task")
def twitter_process_goal_task(goal_id: str):
    """Process a single goal event - can run concurrently"""
    logger = get_run_logger()
    
    logger.info(f"🎯 Processing individual goal: {goal_id}")
    
    try:
        goal_doc = store.goals_pending.find_one({"_id": goal_id})  # ✅ UPDATED
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_pending")  # ✅ UPDATED
            return {"status": "not_found", "goal_id": goal_id}
        
        logger.info(f"🚨 GOAL FOUND: {goal_doc['team_name']} - {goal_doc['player_name']} ({goal_doc['minute']}')")
        
        # Simulate Twitter posting
        tweet_text = f"⚽ GOAL! {goal_doc['player_name']} scores for {goal_doc['team_name']} in the {goal_doc['minute']}' minute!"
        logger.info(f"🐦 TWITTER: {tweet_text}")
        
        # Move goal from pending to processed
        goal_doc["processed_at"] = datetime.now(timezone.utc)
        goal_doc["twitter_status"] = "posted"
        
        store.goals_processed.replace_one({"_id": goal_id}, goal_doc, upsert=True)
        store.goals_pending.delete_one({"_id": goal_id})  # ✅ UPDATED
        
        logger.info(f"✅ Goal {goal_id} processed and moved to goals_processed")
        
        return {
            "status": "success",
            "goal_id": goal_id,
            "tweet_text": tweet_text,
            "team": goal_doc['team_name'],
            "player": goal_doc['player_name'],
            "minute": goal_doc['minute']
        }
        
    except Exception as e:
        logger.error(f"❌ Error processing goal {goal_id}: {e}")
        return {"status": "error", "goal_id": goal_id, "error": str(e)}

@flow(name="twitter-flow")
def twitter_flow(goal_id: Optional[str] = None):
    """Twitter flow - name set by direct triggering"""
    logger = get_run_logger()
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided")
        return {"status": "error", "message": "No goal_id provided"}
    
    logger.info(f"🔍 Processing goal: {goal_id}")
    
    # Process the goal
    goal_result = twitter_process_goal_task(goal_id)
    
    logger.info(f"✅ Twitter processing completed for goal {goal_id}")
    
    return {
        "goal_id": goal_id,
        "goal_result": goal_result,
        "status": "completed"
    }