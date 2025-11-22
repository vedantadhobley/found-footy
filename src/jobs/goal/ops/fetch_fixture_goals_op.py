"""Fetch fixture goals from API"""
from typing import Any, Dict

from dagster import Config, OpExecutionContext, op

from src.api.mongo_api import fixtures_batch


class FetchFixtureGoalsConfig(Config):
    """Configuration for fetch_fixture_goals_op"""
    fixture_id: int


@op(
    name="fetch_fixture_goals",
    description="Fetch all goal events for fixture from API",
)
def fetch_fixture_goals_op(
    context: OpExecutionContext,
    config: FetchFixtureGoalsConfig
) -> Dict[str, Any]:
    """
    Fetch fixture data and extract goal events.
    
    Returns:
        dict: {
            "fixture_id": int,
            "fixture_status": str,
            "goal_events": List[Dict] - raw goal events from API
        }
    """
    fixture_id = config.fixture_id
    
    context.log.info(f"Fetching fixture {fixture_id} from API")
    
    # Fetch from API
    fixture_results = fixtures_batch([fixture_id])
    if not fixture_results:
        context.log.error(f"Could not fetch fixture {fixture_id} from API")
        return {
            "fixture_id": fixture_id,
            "fixture_status": None,
            "goal_events": []
        }
    
    fixture_data = fixture_results[0]
    
    # Extract goal events (filter out missed penalties)
    events = fixture_data.get("events", [])
    goal_events = [
        e for e in events 
        if e.get("type") == "Goal" and e.get("detail") != "Missed Penalty"
    ]
    
    fixture_status = fixture_data.get("fixture", {}).get("status", {}).get("short")
    
    context.log.info(f"Found {len(goal_events)} goal events for fixture {fixture_id} (status: {fixture_status})")
    
    return {
        "fixture_id": fixture_id,
        "fixture_status": fixture_status,
        "fixture_data": fixture_data,
        "goal_events": goal_events
    }
