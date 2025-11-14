"""Process goal op - validates and stores a single goal

Clean approach: Only pass goal_id via config, read full data from MongoDB.
This matches Prefect pattern where MongoDB is the source of truth.
"""

from dagster import op, Config
from typing import Dict, Any
from pymongo import MongoClient
from bson import ObjectId
from datetime import datetime


class GoalProcessingConfig(Config):
    """Minimal config - just identifiers, MongoDB has the data."""
    fixture_id: str = "000000000000000000000000"  # MongoDB ObjectId as string (default for testing)
    goal_minute: int = 45  # Minute when goal was scored
    player_name: str = "Test Player"  # Player who scored (for unique identification)
    mongo_uri: str = "mongodb://localhost:27017"
    db_name: str = "found_footy"


@op(
    name="process_goal",
    description="Store a single goal in MongoDB (reads goal data from fixtures collection)"
)
def process_goal_op(context, config: GoalProcessingConfig) -> Dict[str, Any]:
    """
    Process a single goal event - matches goal_flow.py from Prefect.
    
    Clean approach:
    1. Receive minimal identifiers via config (fixture_id, minute, player)
    2. Read full goal event data from fixtures collection in MongoDB
    3. Store in goals collection if new
    4. Return goal_id for downstream processing
    
    This op processes ONE goal per pipeline run (enables parallel processing).
    """
    client = MongoClient(config.mongo_uri)
    db = client[config.db_name]
    
    context.log.info(f"Processing goal: {config.player_name} ({config.goal_minute}') from fixture {config.fixture_id}")
    
    # Check if goal already exists in goals collection
    existing = db.goals.find_one({
        "fixture_id": ObjectId(config.fixture_id),
        "time.elapsed": config.goal_minute,
        "player.name": config.player_name
    })
    
    if existing:
        goal_id = str(existing["_id"])
        context.log.info(f"✅ Goal already exists: {goal_id}")
        client.close()
        return {
            "goal_id": goal_id,
            "is_new": False,
            "player": config.player_name,
            "minute": config.goal_minute,
            "fixture_id": config.fixture_id
        }
    
    # Read full goal event from fixtures collection
    fixture = db.fixtures.find_one({"_id": ObjectId(config.fixture_id)})
    if not fixture:
        context.log.error(f"❌ Fixture {config.fixture_id} not found in MongoDB")
        client.close()
        raise ValueError(f"Fixture {config.fixture_id} not found")
    
    # Find the specific goal event in the goals array
    goals_array = fixture.get("goals", [])
    goal_event = None
    
    for event in goals_array:
        if (event.get("time", {}).get("elapsed") == config.goal_minute and 
            event.get("player", {}).get("name") == config.player_name):
            goal_event = event
            break
    
    if not goal_event:
        context.log.error(f"❌ Goal event not found for {config.player_name} at {config.goal_minute}'")
        client.close()
        raise ValueError(f"Goal event not found for {config.player_name} at {config.goal_minute}'")
    
    # Extract team info from fixture
    home_team = fixture.get("teams", {}).get("home", {}).get("name", "Unknown")
    away_team = fixture.get("teams", {}).get("away", {}).get("name", "Unknown")
    
    context.log.info(f"📖 Read goal data from fixtures: {config.player_name} in {home_team} vs {away_team}")
    
    # Create new goal document
    goal_doc = {
        "fixture_id": ObjectId(config.fixture_id),
        "time": goal_event["time"],
        "team": goal_event["team"],
        "player": goal_event["player"],
        "assist": goal_event.get("assist"),
        "type": goal_event["type"],
        "detail": goal_event["detail"],
        "comments": goal_event.get("comments"),
        "processing_status": {
            "twitter_scraped": False,
            "videos_downloaded": False,
            "videos_uploaded": False,
            "videos_filtered": False,
            "completed": False
        },
        "created_at": datetime.utcnow(),
        "home_team": home_team,  # Store for easy access
        "away_team": away_team
    }
    
    result = db.goals.insert_one(goal_doc)
    goal_id = str(result.inserted_id)
    
    context.log.info(f"🆕 NEW GOAL STORED: {config.player_name} ({config.goal_minute}') [{goal_id}]")
    
    client.close()
    
    return {
        "goal_id": goal_id,
        "is_new": True,
        "player": config.player_name,
        "minute": config.goal_minute,
        "fixture_id": config.fixture_id,
        "home_team": home_team,
        "away_team": away_team
    }
