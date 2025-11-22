"""Fetch fixtures from api-football.com"""
from datetime import date
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.api.mongo_api import fixtures
from src.utils.team_data import get_team_ids


@op(
    name="fetch_fixtures",
    description="Fetch all fixtures for a specific date from api-football.com",
    tags={"kind": "api", "source": "api-football"}
)
def fetch_fixtures_op(context: OpExecutionContext, target_date: date | None = None) -> List[Dict[str, Any]]:
    """
    Fetch fixtures for the given date (defaults to today).
    
    Args:
        target_date: Date to fetch fixtures for (defaults to today)
        
    Returns:
        List of fixture objects from API
    """
    if target_date is None:
        target_date = date.today()
    
    context.log.info(f"🌐 Fetching fixtures for {target_date}")
    
    try:
        # Get all fixtures for the date
        all_fixtures = fixtures(target_date)
        context.log.info(f"📥 Retrieved {len(all_fixtures)} total fixtures from API")
        
        # Filter to only our tracked teams (50 teams: 25 UEFA + 25 FIFA)
        tracked_team_ids = set(get_team_ids())
        
        filtered_fixtures = [
            fixture for fixture in all_fixtures
            if fixture.get("teams", {}).get("home", {}).get("id") in tracked_team_ids
            or fixture.get("teams", {}).get("away", {}).get("id") in tracked_team_ids
        ]
        
        context.log.info(
            f"✅ Filtered to {len(filtered_fixtures)} fixtures for our {len(tracked_team_ids)} tracked teams "
            f"(removed {len(all_fixtures) - len(filtered_fixtures)} irrelevant fixtures)"
        )
        
        return filtered_fixtures
    
    except Exception as e:
        context.log.error(f"❌ Failed to fetch fixtures: {e}")
        raise
