from prefect import flow, task, get_run_logger
from prefect.runtime import flow_run
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
        goal_doc = store.goals_active.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_active")
            return {"status": "not_found", "goal_id": goal_id}
        
        logger.info(f"🚨 GOAL FOUND: {goal_doc['team_name']} - {goal_doc['player_name']} ({goal_doc['minute']}')")
        
        # ✅ Simulate Twitter posting
        tweet_text = f"⚽ GOAL! {goal_doc['player_name']} scores for {goal_doc['team_name']} in the {goal_doc['minute']}' minute!"
        logger.info(f"🐦 TWITTER: {tweet_text}")
        
        # ✅ Move goal from active to processed
        goal_doc["processed_at"] = datetime.now(timezone.utc)
        goal_doc["twitter_status"] = "posted"
        
        store.goals_processed.replace_one({"_id": goal_id}, goal_doc, upsert=True)
        store.goals_active.delete_one({"_id": goal_id})
        
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

@flow(name="twitter-search-flow")
def twitter_search_flow(goal_id: Optional[str] = None):
    """First step of Twitter process - search and process individual goals"""
    logger = get_run_logger()
    
    # ✅ DYNAMICALLY SET FLOW RUN NAME
    if goal_id:
        try:
            # Try to get goal details from database first
            goal_doc = store.goals_active.find_one({"_id": goal_id})
            if goal_doc:
                # Rich contextual name with actual data
                new_name = f"⚽ {goal_doc['team_name']}: {goal_doc['player_name']} ({goal_doc['minute']}') [#{goal_doc['fixture_id']}]"
            else:
                # Fallback if goal not found in DB
                parts = goal_id.split('_')
                if len(parts) >= 3:
                    fixture_id = parts[0]
                    minute = parts[1]
                    new_name = f"🔍 TWITTER: Goal {minute}' [#{fixture_id}] ({goal_id})"
                else:
                    new_name = f"🔍 TWITTER: Goal {goal_id}"
            
            # ✅ CRUCIAL: Set the flow run name dynamically
            flow_run.name = new_name
            logger.info(f"📝 Set flow name: {new_name}")
            
        except Exception as e:
            # Fallback name if anything goes wrong
            fallback_name = f"🔍 TWITTER: Goal {goal_id}"
            flow_run.name = fallback_name
            logger.warning(f"⚠️ Error setting rich name ({e}), using: {fallback_name}")
    else:
        flow_run.name = "🔍 TWITTER: No Goal ID"
        logger.warning("⚠️ No goal_id provided to twitter_search_flow")
        return {"status": "error", "message": "No goal_id provided"}
    
    # ✅ STEP 1: Process the specific goal (search step)
    goal_result = twitter_process_goal_task(goal_id)
    
    logger.info(f"✅ Twitter search flow completed for goal {goal_id}")
    
    return {
        "goal_id": goal_id,
        "goal_result": goal_result,
        "status": "completed"
    }