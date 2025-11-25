"""Test job - Insert baseline fixture and trigger monitor to test full pipeline

This job matches the proven testing methodology:
1. Insert fixture directly into fixtures_active with 0-0 baseline
2. Trigger monitor job immediately
3. Monitor fetches real API data (with Szoboszlai goal)
4. Monitor detects goal change and triggers goal pipeline

IMPORTANT: Twitter authentication required!

The service will try to use saved cookies on startup. If no cookies exist or they're invalid,
you'll need to login once:

    ./scripts/login_twitter.sh

This saves cookies to a persistent volume, so you only need to login once.
Check status: http://localhost:8888/health (should show authenticated: true)

Run this from Dagster UI to test the entire flow!
"""

from dagster import op, job, Config, OpExecutionContext
from pymongo import MongoClient
from typing import Dict, Any
import requests


class TestConfig(Config):
    """Test configuration - uses Liverpool vs Real Madrid UCL match (Nov 4, 2025)"""
    mongo_uri: str = "mongodb://ffuser:ffpass@mongo:27017/found_footy?authSource=admin"
    db_name: str = "found_footy"
    # Real fixture ID: Liverpool 1-0 Real Madrid (UCL 2025-11-04)
    fixture_id: int = 1451077
    home_team: str = "Liverpool"
    away_team: str = "Real Madrid"
    home_team_id: int = 40
    away_team_id: int = 541


@op(
    name="insert_baseline_fixture",
    description="Insert baseline fixture with 0-0 into fixtures_active"
)
def insert_baseline_fixture_op(context: OpExecutionContext, config: TestConfig) -> Dict[str, Any]:
    """Insert baseline fixture directly into fixtures_active with 0-0 score
    
    This matches the proven test pattern - insert with no goals, then let
    monitor fetch real API data and detect the change.
    """
    
    # Check Twitter authentication status
    try:
        response = requests.get("http://twitter-session:8888/health", timeout=2)
        health = response.json()
        if not health.get("authenticated"):
            context.log.warning("⚠️ Twitter is NOT authenticated!")
            context.log.warning("   Run: ./scripts/login_twitter.sh")
            context.log.warning("   Twitter search will fail until you login")
        else:
            context.log.info("✅ Twitter is authenticated - ready for video search")
    except Exception as e:
        context.log.warning(f"⚠️ Could not check Twitter status: {e}")
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    # Clean up any existing test data first
    context.log.info(f"🧹 Cleaning up existing test data for fixture {config.fixture_id}")
    db.fixtures_active.delete_many({"_id": config.fixture_id})
    db.goals.delete_many({"fixture_id": config.fixture_id})
    
    # Insert baseline fixture (0-0) directly into fixtures_active
    baseline_fixture = {
        "_id": config.fixture_id,
        "fixture": {
            "id": config.fixture_id,
            "referee": "Istvan Kovacs, Romania",
            "timezone": "UTC",
            "date": "2025-11-04T20:00:00+00:00",
            "timestamp": 1762286400,
            "periods": {
                "first": 1762286400,
                "second": 1762290000
            },
            "venue": {
                "id": 550,
                "name": "Anfield",
                "city": "Liverpool"
            },
            "status": {
                "long": "Match Finished",
                "short": "FT",  # Full time
                "elapsed": 90,
                "extra": 6
            }
        },
        "league": {
            "id": 2,
            "name": "UEFA Champions League",
            "country": "World",
            "logo": "https://media.api-sports.io/football/leagues/2.png",
            "flag": None,
            "season": 2025,
            "round": "League Stage - 4"
        },
        "teams": {
            "home": {
                "id": config.home_team_id,
                "name": config.home_team,
                "logo": "https://media.api-sports.io/football/teams/40.png",
                "winner": True
            },
            "away": {
                "id": config.away_team_id,
                "name": config.away_team,
                "logo": "https://media.api-sports.io/football/teams/541.png",
                "winner": False
            }
        },
        "goals": {
            "home": 0,  # Baseline: 0-0 (API will return 1-0)
            "away": 0
        },
        "score": {
            "halftime": {"home": 0, "away": 0},
            "fulltime": {"home": 0, "away": 0},  # Baseline 0-0
            "extratime": {"home": None, "away": None},
            "penalty": {"home": None, "away": None}
        }
    }
    
    db.fixtures_active.replace_one(
        {"_id": config.fixture_id}, 
        baseline_fixture, 
        upsert=True
    )
    
    context.log.info(f"✅ Baseline inserted into fixtures_active: {config.home_team} 0-0 {config.away_team}")
    context.log.info(f"   Fixture ID: {config.fixture_id}")
    context.log.info(f"   Competition: UEFA Champions League")
    context.log.info(f"   Venue: Anfield, Liverpool")
    context.log.info(f"   Status: FT (finished)")
    context.log.info(f"   Real result: Liverpool 1-0 Real Madrid (monitor will detect this)")
    
    client.close()
    
    return {
        "fixture_id": config.fixture_id,
        "home_team": config.home_team,
        "away_team": config.away_team
    }


@op(
    name="trigger_monitor_job",
    description="Trigger monitor job to fetch API data and detect goal change"
)
def trigger_monitor_job_op(context: OpExecutionContext, config: TestConfig, insert_result: Dict) -> Dict[str, Any]:
    """Trigger monitor job immediately to process the baseline fixture
    
    Monitor will:
    1. Fetch real API data for this fixture
    2. Detect goal change (0-0 baseline vs real 1-0 from API)
    3. Trigger goal pipeline for each new goal
    """
    
    fixture_id = insert_result["fixture_id"]
    
    # Import monitor job to trigger it directly
    from src.jobs.monitor import monitor_fixtures_job
    
    context.log.info(f"🚀 Triggering monitor job for fixture {fixture_id}...")
    context.log.info(f"   Match: Liverpool vs Real Madrid (UCL)")
    context.log.info(f"   Monitor will fetch real API data")
    context.log.info(f"   Expected: API returns 1 goal (Liverpool 1-0 Real Madrid)")
    context.log.info(f"   Monitor will detect: 0-0 baseline → 1-0 from API")
    
    # Trigger monitor job synchronously
    result = monitor_fixtures_job.execute_in_process(
        instance=context.instance,
        tags={
            "trigger": "test_pipeline",
            "test_fixture_id": str(fixture_id)
        }
    )
    
    if result.success:
        context.log.info("✅ Monitor job completed successfully!")
        context.log.info("   Check logs above for goal pipeline triggers")
    else:
        context.log.error("❌ Monitor job failed - check monitor logs for details")
    
    return {
        "fixture_id": fixture_id,
        "monitor_success": result.success,
        "home_team": insert_result["home_team"],
        "away_team": insert_result["away_team"]
    }


@op(
    name="verify_results",
    description="Verify goals were detected and processed"
)
def verify_results_op(context: OpExecutionContext, config: TestConfig, monitor_result: Dict) -> Dict[str, Any]:
    """Check that monitor detected goals and triggered goal pipeline"""
    
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    fixture_id = monitor_result["fixture_id"]
    
    context.log.info(f"📊 Checking results for fixture {fixture_id}...")
    
    # Check if fixture was updated by monitor
    fixture = db.fixtures_active.find_one({"_id": fixture_id})
    
    if not fixture:
        context.log.error(f"❌ Fixture {fixture_id} not found in fixtures_active!")
        client.close()
        return {"success": False}
    
    # Check goal counts
    home_goals = fixture.get("goals", {}).get("home", 0)
    away_goals = fixture.get("goals", {}).get("away", 0)
    
    context.log.info(f"   Fixture score: {home_goals}-{away_goals}")
    
    if home_goals == 0 and away_goals == 0:
        context.log.warning("⚠️ Fixture still 0-0 - monitor may not have detected change")
        context.log.info("   This could mean:")
        context.log.info("   1. API didn't return goal data")
        context.log.info("   2. Monitor's change detection logic didn't trigger")
        context.log.info("   3. Network/API issues")
    else:
        context.log.info(f"✅ Monitor updated fixture: {home_goals}-{away_goals}")
    
    # Check goals collection
    goals = list(db.goals.find({"fixture_id": fixture_id}))
    
    context.log.info(f"   Goals in collection: {len(goals)}")
    
    if goals:
        context.log.info("🎯 SUCCESS! Goals were processed:")
        for goal in goals:
            player_name = goal.get("player", {}).get("name", "Unknown")
            minute = goal.get("time", {}).get("elapsed", "?")
            status = goal.get("processing_status", "unknown")
            context.log.info(f"   🥅 {player_name} - {minute}' (status: {status})")
        
        context.log.info("\n✅ Test pipeline completed successfully!")
        context.log.info("   Monitor detected goals and triggered goal pipeline")
    else:
        context.log.warning("⚠️ No goals in collection - check monitor logs")
    
    client.close()
    
    return {
        "fixture_id": fixture_id,
        "goals_detected": len(goals),
        "success": len(goals) > 0
    }


@job(
    name="test_pipeline",
    description="Test job: Insert baseline fixture -> Trigger monitor -> Verify goal processing"
)
def test_pipeline_job():
    """
    Test the complete flow using proven methodology:
    
    Fixture: Liverpool 1-0 Real Madrid (UCL 2025-11-04)
    Fixture ID: 1451077
    
    1. Insert baseline fixture (0-0) into fixtures_active
    2. Trigger monitor job immediately
    3. Monitor fetches real API data (1 goal)
    4. Monitor detects change and triggers goal pipeline
    5. Verify goals were processed
    
    This matches the working Prefect test pattern.
    """
    insert_result = insert_baseline_fixture_op()
    monitor_result = trigger_monitor_job_op(insert_result)
    verify_results_op(monitor_result)


__all__ = ["test_pipeline_job"]
