"""Fetch goal events for a fixture"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.api.mongo_api import fixtures_events


@op(
    name="fetch_goal_events",
    description="Fetch all events for a fixture from api-football.com",
    tags={"kind": "api", "source": "api-football"}
)
def fetch_goal_events_op(context: OpExecutionContext, fixture_id: int) -> List[Dict[str, Any]]:
    """
    Fetch all events for a fixture and filter for goal events.
    
    Args:
        fixture_id: The fixture ID to fetch events for
        
    Returns:
        List of goal event objects (type=Goal, excluding Missed Penalty)
    """
    context.log.info(f"🌐 Fetching events for fixture {fixture_id}")
    
    try:
        all_events = fixtures_events(fixture_id)
        
        # Filter for actual goals (exclude missed penalties)
        goal_events = [
            event for event in all_events
            if event.get("type") == "Goal" and event.get("detail") != "Missed Penalty"
        ]
        
        context.log.info(f"✅ Found {len(goal_events)} goal events (out of {len(all_events)} total events)")
        
        # Log goal details
        for event in goal_events:
            player = event.get("player", {}).get("name", "Unknown")
            minute = event.get("time", {}).get("elapsed", "?")
            extra = event.get("time", {}).get("extra")
            team = event.get("team", {}).get("name", "Unknown")
            detail = event.get("detail", "")
            
            minute_str = f"{minute}+{extra}" if extra else str(minute)
            context.log.info(f"   ⚽ {minute_str}' - {player} ({team}) [{detail}]")
        
        return goal_events
    
    except Exception as e:
        context.log.error(f"❌ Failed to fetch events: {e}")
        raise
