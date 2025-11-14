"""Sensor to trigger goal_pipeline when monitor detects goals

Clean approach:
- Monitor (runs every 5min) detects goals and updates fixtures in MongoDB
- Sensor (checks every 60s) finds unprocessed goals
- Passes minimal data via config: fixture_id, minute, player_name
- Ops read full goal data from MongoDB (source of truth)
- One pipeline run per goal (parallel processing)

No delay logic - sensor triggers immediately when goals detected.
Future: Add retry logic for goals with insufficient videos.
"""

from dagster import sensor, RunRequest, SensorEvaluationContext
from pymongo import MongoClient
from bson import ObjectId


@sensor(
    name="goal_pipeline_trigger",
    minimum_interval_seconds=60,  # Check every minute
    description="Watches for unprocessed goals and triggers one pipeline per goal"
)
def goal_pipeline_trigger_sensor(context: SensorEvaluationContext):
    """
    Polls MongoDB for fixtures with goals that need processing.
    Triggers ONE pipeline run per goal for parallel processing.
    
    Flow:
    1. Monitor detects goals every 5min → updates fixtures in MongoDB
    2. Sensor finds goals not yet in goals collection
    3. Triggers pipeline with minimal config (fixture_id + identifiers)
    4. Pipeline reads full data from MongoDB
    """
    client = MongoClient("mongodb://localhost:27017")
    db = client["found_footy"]
    
    # Find fixtures with goals that need processing
    # Only look at completed fixtures (FT, AET, PEN)
    fixtures = db.fixtures.find({
        "goals": {"$exists": True, "$ne": []},
        "fixture.status.short": {"$in": ["FT", "AET", "PEN"]}
    }).limit(20)  # Process up to 20 fixtures at a time
    
    for fixture in fixtures:
        fixture_id = str(fixture["_id"])
        goals = fixture.get("goals", [])
        
        if not goals:
            continue
        
        # Get fixture metadata for logging
        home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
        away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
        
        # Process each goal individually
        for goal_event in goals:
            # Extract goal identifiers
            minute = goal_event.get("time", {}).get("elapsed", 0)
            player_name = goal_event.get("player", {}).get("name")
            
            # Skip goals without player name (incomplete data)
            if not player_name:
                context.log.warning(f"⚠️ Skipping goal at {minute}' - missing player name")
                continue
            
            # Check if this specific goal already exists in goals collection
            existing_goal = db.goals.find_one({
                "fixture_id": ObjectId(fixture_id),
                "time.elapsed": minute,
                "player.name": player_name
            })
            
            # Only trigger pipeline if goal doesn't exist
            if not existing_goal:
                # Trigger ONE pipeline run for this ONE goal
                # Pass minimal data via config - op will read full data from MongoDB
                mongo_uri = "mongodb://localhost:27017"
                db_name = "found_footy"
                
                yield RunRequest(
                    run_key=f"goal_{fixture_id}_{minute}_{player_name.replace(' ', '_')}",
                    run_config={
                        "ops": {
                            "process_goal": {
                                "config": {
                                    "fixture_id": fixture_id,
                                    "goal_minute": minute,
                                    "player_name": player_name,
                                    "mongo_uri": mongo_uri,
                                    "db_name": db_name
                                }
                            },
                            "scrape_twitter": {
                                "config": {
                                    "mongo_uri": mongo_uri,
                                    "db_name": db_name
                                }
                            },
                            "download_videos": {
                                "config": {
                                    "mongo_uri": mongo_uri,
                                    "db_name": db_name
                                }
                            },
                            "upload_videos": {
                                "config": {
                                    "mongo_uri": mongo_uri,
                                    "db_name": db_name
                                }
                            },
                            "filter_videos": {
                                "config": {
                                    "mongo_uri": mongo_uri,
                                    "db_name": db_name
                                }
                            }
                        }
                    },
                    tags={
                        "fixture_id": fixture_id,
                        "player": player_name,
                        "minute": str(minute),
                        "home_team": home_team,
                        "away_team": away_team
                    }
                )
                
                context.log.info(f"✅ Triggering pipeline: {player_name} ({minute}') in {home_team} vs {away_team}")
    
    client.close()


__all__ = ["goal_pipeline_trigger_sensor"]
