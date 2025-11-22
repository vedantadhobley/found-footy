"""Detect goal deltas between stored and fetched fixture data"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.data.mongo_store import FootyMongoStore


@op(
    name="detect_goal_delta",
    description="Compare stored vs fetched goal counts to detect new goals",
    tags={"kind": "logic", "purpose": "delta_detection"}
)
def detect_goal_delta_op(
    context: OpExecutionContext,
    fresh_fixtures: List[Dict[str, Any]]
) -> List[Dict[str, Any]]:
    """
    Compare goal counts between stored fixtures and fresh API data.
    Identify fixtures with new goals.
    
    NOTE: We do NOT update fixture data here - that happens after goals are validated.
    
    Args:
        fresh_fixtures: Fresh fixture data from API
        
    Returns:
        List of fixtures with new goals, each containing:
        - fixture_id
        - stored_goals: {home, away, total}
        - fetched_goals: {home, away, total}
        - goal_delta: number of new goals
        - fresh_data: the fresh API data
    """
    store = FootyMongoStore()
    
    fixtures_with_new_goals = []
    
    for fresh_fixture in fresh_fixtures:
        try:
            fixture_id = fresh_fixture["fixture"]["id"]
            
            # Get stored fixture
            stored_fixture = store.fixtures_active.find_one({"_id": fixture_id})
            
            if not stored_fixture:
                context.log.warning(f"⚠️  Fixture {fixture_id} not found in active collection")
                continue
            
            # Extract goal counts
            stored_home = stored_fixture.get("goals", {}).get("home") or 0
            stored_away = stored_fixture.get("goals", {}).get("away") or 0
            stored_total = stored_home + stored_away
            
            fetched_home = fresh_fixture.get("goals", {}).get("home") or 0
            fetched_away = fresh_fixture.get("goals", {}).get("away") or 0
            fetched_total = fetched_home + fetched_away
            
            # Check for goal delta
            goal_delta = fetched_total - stored_total
            
            if goal_delta > 0:
                home_team = fresh_fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
                away_team = fresh_fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
                
                context.log.info(
                    f"🚨 GOAL DELTA: Fixture {fixture_id} ({home_team} vs {away_team}): "
                    f"{stored_home}-{stored_away} → {fetched_home}-{fetched_away} (+{goal_delta} goals)"
                )
                
                fixtures_with_new_goals.append({
                    "fixture_id": fixture_id,
                    "stored_goals": {
                        "home": stored_home,
                        "away": stored_away,
                        "total": stored_total
                    },
                    "fetched_goals": {
                        "home": fetched_home,
                        "away": fetched_away,
                        "total": fetched_total
                    },
                    "goal_delta": goal_delta,
                    "fresh_data": fresh_fixture
                })
        
        except Exception as e:
            context.log.error(f"❌ Error processing fixture delta: {e}")
            continue
    
    context.log.info(f"🎯 Detected {len(fixtures_with_new_goals)} fixtures with new goals")
    
    return fixtures_with_new_goals
