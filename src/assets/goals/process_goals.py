"""Goal processing asset - handles discovered goals"""

import logging
from typing import List

from dagster import asset, AssetExecutionContext, Output, Config
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


class ProcessGoalsConfig(Config):
    fixture_id: int
    goal_events: List[dict] | None = None


@asset(
    name="process_goals",
    description="Process goal events and schedule Twitter scraping",
    group_name="goals",
    compute_kind="processing"
)
def process_goals_asset(
    context: AssetExecutionContext,
    config: ProcessGoalsConfig
) -> Output:
    """
    Process goal events - migrated from goal_flow.py
    
    Steps:
    1. Get discovered goals from MongoDB
    2. Validate goal data
    3. Update processing status
    4. Schedule downstream Twitter scraping
    """
    if not goal_events:
        context.log.warning(f"No goal events for fixture {fixture_id}")
        return Output(value={"status": "no_goals", "fixture_id": fixture_id})
    
    # Filter for actual goals (not assists, penalties, etc.)
    actual_goals = [event for event in goal_events if event.get("type") == "Goal"]
    
    if not actual_goals:
        context.log.info(f"⚽ No actual goals in {len(goal_events)} events")
        return Output(value={"status": "no_goals", "fixture_id": fixture_id})
    
    context.log.info(f"⚽ Processing {len(actual_goals)} goal events for fixture {fixture_id}")
    
    store = FootyMongoStore()
    existing_goal_ids = store.get_existing_goal_ids(fixture_id)
    
    new_goals = []
    updated_goals = []
    
    for goal_event in actual_goals:
        try:
            time_data = goal_event.get("time", {})
            elapsed = time_data.get("elapsed", 0)
            extra = time_data.get("extra")
            
            goal_id = f"{fixture_id}_{elapsed}+{extra}" if extra else f"{fixture_id}_{elapsed}"
            is_new_goal = goal_id not in existing_goal_ids
            
            if is_new_goal:
                if store.store_goal(fixture_id, goal_event, processing_status="discovered"):
                    player_name = goal_event.get("player", {}).get("name", "Unknown")
                    team_name = goal_event.get("team", {}).get("name", "Unknown")
                    display_minute = f"{elapsed}+{extra}" if extra else str(elapsed)
                    
                    context.log.info(f"🆕 NEW GOAL: {team_name} - {player_name} ({display_minute}') [{goal_id}]")
                    new_goals.append(goal_id)
            else:
                store.store_goal(fixture_id, goal_event, processing_status="discovered")
                context.log.info(f"🔄 UPDATED GOAL: {goal_id}")
                updated_goals.append(goal_id)
                
        except Exception as e:
            context.log.error(f"Error processing goal event: {e}")
    
    result = {
        "status": "success",
        "fixture_id": fixture_id,
        "new_goals": len(new_goals),
        "updated_goals": len(updated_goals)
    }
    
    return Output(
        value=result,
        metadata={
            "new_goals": len(new_goals),
            "updated_goals": len(updated_goals)
        }
    )
