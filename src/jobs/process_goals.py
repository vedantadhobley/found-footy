"""Process goals job - validates and stores goal events

Migrated from found_footy/flows/goal_flow.py
"""

import logging
from typing import List, Dict, Any

from dagster import job, op, OpExecutionContext, Config, Out
from src.data.mongo_store import FootyMongoStore

logger = logging.getLogger(__name__)


class ProcessGoalsConfig(Config):
    """Configuration for processing goals"""
    fixture_id: int
    goal_events: List[Dict[str, Any]]


@op(
    name="validate_and_store_goals",
    description="Validate goal events and store to MongoDB",
    out=Out(List[str], description="List of new goal IDs that need Twitter scraping")
)
def validate_and_store_goals_op(
    context: OpExecutionContext,
    config: ProcessGoalsConfig
) -> List[str]:
    """
    Process goal events:
    1. Filter for actual goals (not assists/penalties)
    2. Store new goals to MongoDB
    3. Return list of goal IDs that need Twitter scraping
    """
    fixture_id = config.fixture_id
    goal_events = config.goal_events
    
    if not goal_events:
        context.log.warning(f"No goal events for fixture {fixture_id}")
        return []
    
    # Filter for actual goals
    actual_goals = [event for event in goal_events if event.get("type") == "Goal"]
    
    if not actual_goals:
        context.log.info(f"⚽ No actual goals in {len(goal_events)} events")
        return []
    
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
    
    context.log.info(f"✅ Processed: {len(new_goals)} new, {len(updated_goals)} updated")
    
    return new_goals


@job(
    name="process_goals",
    description="Process goal events and prepare for Twitter scraping"
)
def process_goals_job():
    """Goal processing workflow"""
    validate_and_store_goals_op()
