"""Sensor to trigger goal_pipeline when monitor detects goals

This sensor polls MongoDB for pending goal processing work left by monitor_fixtures,
then triggers goal_pipeline_job for each fixture with goals.
"""

from dagster import sensor, RunRequest, SensorEvaluationContext
from pymongo import MongoClient
from bson import ObjectId


@sensor(
    name="goal_pipeline_trigger",
    minimum_interval_seconds=60,  # Check every minute
    description="Watches for fixtures with pending goals and triggers goal_pipeline"
)
def goal_pipeline_trigger_sensor(context: SensorEvaluationContext):
    """
    Polls MongoDB for fixtures with goals that need processing.
    Triggered by monitor_fixtures storing pending work.
    """
    client = MongoClient("mongodb://localhost:27017")
    db = client["found_footy"]
    
    # Find fixtures with goals that need processing
    # These are stored by monitor_fixtures_job
    fixtures = db.fixtures.find({
        "goals": {"$exists": True, "$ne": []},
        "fixture.status.short": {"$in": ["FT", "AET", "PEN"]}
    }).limit(20)  # Process up to 20 fixtures at a time
    
    for fixture in fixtures:
        fixture_id = str(fixture["_id"])
        goals = fixture.get("goals", [])
        
        if not goals:
            continue
        
        # Check if we've already processed these goals
        # Count existing goals for this fixture
        existing_goal_count = db.goals.count_documents({
            "fixture_id": ObjectId(fixture_id)
        })
        
        # If we have fewer goals stored than in fixture, process
        if len(goals) > existing_goal_count:
            # Trigger goal_pipeline for this fixture
            yield RunRequest(
                run_key=f"goal_pipeline_{fixture_id}_{len(goals)}",
                run_config={
                    "ops": {
                        "process_goals": {
                            "config": {
                                "mongo_uri": "mongodb://localhost:27017",
                                "db_name": "found_footy"
                            },
                            "inputs": {
                                "fixture_id": fixture_id,
                                "goal_events": goals
                            }
                        }
                    }
                },
                tags={
                    "fixture_id": fixture_id,
                    "goal_count": str(len(goals))
                }
            )
            
            context.log.info(f"Triggering goal_pipeline for fixture {fixture_id} with {len(goals)} goals")
    
    client.close()


__all__ = ["goal_pipeline_trigger_sensor"]
