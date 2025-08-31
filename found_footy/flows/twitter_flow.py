from prefect import flow, task, get_run_logger
from found_footy.storage.mongo_store import FootyMongoStore
from datetime import datetime, timezone
from typing import Optional  # ✅ ADD: Missing import for Optional

store = FootyMongoStore()

@task(name="process-individual-goal")
def process_individual_goal(goal_id: str):
    """Process a single goal event - can run concurrently"""
    logger = get_run_logger()
    
    logger.info(f"🎯 Processing individual goal: {goal_id}")
    
    try:
        # Get goal from goals_active
        goal_doc = store.goals_active.find_one({"_id": goal_id})
        
        if not goal_doc:
            logger.warning(f"⚠️ Goal {goal_id} not found in goals_active")
            return {"status": "not_found", "goal_id": goal_id}
        
        logger.info(f"🚨 GOAL FOUND: {goal_doc['team_name']} - {goal_doc['player_name']} ({goal_doc['minute']}')")
        logger.info(f"⚽ Fixture: {goal_doc['fixture_id']}")
        
        # ✅ Simulate Twitter posting (placeholder for actual Twitter logic)
        tweet_text = f"⚽ GOAL! {goal_doc['player_name']} scores for {goal_doc['team_name']} in the {goal_doc['minute']}' minute!"
        logger.info(f"🐦 TWITTER: {tweet_text}")
        
        # ✅ Move THIS specific goal from active to processed
        goal_doc["processed_at"] = datetime.now(timezone.utc)
        goal_doc["twitter_status"] = "posted"
        
        # Insert into processed collection
        result = store.goals_processed.replace_one(
            {"_id": goal_id}, 
            goal_doc, 
            upsert=True
        )
        
        # Remove from active collection
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

@task(name="advance-completed-fixtures")
def advance_completed_fixtures():
    """Move completed fixtures from active to processed - separate from goal processing"""
    logger = get_run_logger()
    
    logger.info("📋 Checking for completed fixtures to advance...")
    
    # Find completed fixtures in active collection
    completed_fixtures = list(store.fixtures_active.find({"status": "completed"}))
    
    if not completed_fixtures:
        logger.info("⏸️ No completed fixtures to advance")
        return 0
    
    logger.info(f"📋 Found {len(completed_fixtures)} completed fixtures to advance")
    
    # Move each completed fixture
    advanced_count = 0
    for fixture in completed_fixtures:
        try:
            fixture_id = fixture["fixture_id"]
            fixture["advanced_at"] = datetime.now(timezone.utc)
            
            # Insert into processed
            store.fixtures_processed.replace_one(
                {"_id": fixture["_id"]}, 
                fixture, 
                upsert=True
            )
            
            # Remove from active
            store.fixtures_active.delete_one({"_id": fixture["_id"]})
            
            advanced_count += 1
            logger.info(f"✅ Advanced completed fixture {fixture_id}")
            
        except Exception as e:
            logger.warning(f"⚠️ Failed to advance fixture {fixture.get('fixture_id', 'unknown')}: {e}")
    
    logger.info(f"✅ Advanced {advanced_count} completed fixtures")
    return advanced_count

@flow(name="twitter-flow")
def twitter_flow(goal_id: Optional[str] = None):  # ✅ FIXED: Optional[str] instead of str
    """✅ CLEANED UP: Removed unnecessary **kwargs"""
    logger = get_run_logger()
    
    logger.info(f"🐦 Twitter flow started for goal: {goal_id}")
    
    if not goal_id:
        logger.warning("⚠️ No goal_id provided to twitter_flow")
        return {"status": "error", "message": "No goal_id provided"}
    
    # ✅ Process this specific goal only
    goal_result = process_individual_goal(goal_id)
    
    # ✅ Optionally clean up completed fixtures (this can run independently)
    fixtures_advanced = advance_completed_fixtures()
    
    logger.info(f"✅ Twitter flow completed for goal {goal_id}")
    
    return {
        "goal_id": goal_id,
        "goal_result": goal_result,
        "fixtures_advanced": fixtures_advanced,
        "status": "completed"
    }