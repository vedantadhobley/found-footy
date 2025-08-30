from prefect import flow, task, get_run_logger

@task(name="log-goal-event")
def log_goal_event(goal_info: dict):
    """Simple goal event logging - no complexity yet"""
    logger = get_run_logger()
    
    fixture_id = goal_info.get("fixture_id")
    goal_team = goal_info.get("goal_team")
    score = goal_info.get("score", "Unknown")
    goal_number = goal_info.get("goal_number", "Unknown")
    
    logger.info(f"🚨 GOAL EVENT RECEIVED!")
    logger.info(f"   Fixture: {fixture_id}")
    logger.info(f"   Team: {goal_team}")
    logger.info(f"   Score: {score}")
    logger.info(f"   Goal #: {goal_number}")
    
    return f"Goal logged: {goal_team} #{goal_number}"

@flow(name="twitter-flow")
def twitter_flow(goal_info: dict = None, **kwargs):
    """✅ SIMPLIFIED: Just log goal events for now"""
    logger = get_run_logger()
    
    logger.info("🐦 Twitter flow started")
    
    if goal_info:
        result = log_goal_event(goal_info)
        logger.info(f"✅ {result}")
        return {"status": "goal_logged", "result": result}
    
    logger.info("⚠️ No goal info provided")
    return {"status": "no_goal_info"}