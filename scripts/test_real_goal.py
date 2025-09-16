#!/usr/bin/env python3
"""Insert test goal and trigger twitter_flow via Prefect deployment."""

import sys
import os
import argparse
import asyncio
from datetime import datetime, timezone

# export PREFECT_API_URL="http://prefect-server:4200/api"

# Ensure project root is in Python path
sys.path.insert(0, '/app')
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

# Set environment for Docker network
os.environ['MONGODB_URL'] = 'mongodb://footy_admin:footy_secure_pass@mongodb:27017/found_footy?authSource=admin'
os.environ['S3_ENDPOINT_URL'] = 'http://minio:9000'
os.environ['TWITTER_SESSION_URL'] = 'http://twitter-session:8888'

from found_footy.utils.logging import get_logger, log_error_with_trace

logger = get_logger(__name__)

def insert_goal_to_active():
    """Insert the test goal into goals_pending collection with correct schema."""
    try:
        from found_footy.storage.mongo_store import FootyMongoStore

        store = FootyMongoStore()
        fixture_id = 959546
        minute = 44
        goal_id = f"{fixture_id}_{minute}"

        goal_doc = {
            "_id": goal_id,
            "fixture_id": fixture_id,
            "minute": minute,
            "extra": None,
            "time": {"elapsed": minute, "extra": None},
            "team": {
                "id": 26,
                "name": "Argentina",
                "logo": "https://media.api-sports.io/football/teams/26.png"
            },
            "player": {"id": 154, "name": "L. Messi"},
            "assist": {"id": 266, "name": "Á. Di María"},
            "type": "Goal",
            "detail": "Normal Goal",
            "comments": None,
            # Add these fields for Twitter flow compatibility:
            "player_name": "L. Messi",
            "team_name": "Argentina",
            "assist_name": "Á. Di María",
            "status": "processing",  # Twitter flow expects "processing"
            "discovered_videos": [],
            "inserted_at": datetime.now(timezone.utc)
        }

        result = store.goals_pending.replace_one({"_id": goal_id}, goal_doc, upsert=True)
        count = store.goals_pending.count_documents({"_id": goal_id})
        logger.info(f"✅ Inserted goal into goals_pending: {goal_id} (count after insert: {count})")
        if count == 0:
            logger.error("❌ Document not found after insert! Check connection and permissions.")
        return goal_id

    except Exception as e:
        log_error_with_trace(logger, "❌ Failed to insert goal", e)
        return None

async def trigger_twitter_flow(goal_id):
    """Trigger the twitter_flow deployment via Prefect API."""
    try:
        from prefect.client import get_client

        client = get_client()
        deployment = await client.read_deployment_by_name("twitter-flow/twitter-flow")
        flow_run = await client.create_flow_run_from_deployment(
            deployment.id,
            parameters={"goal_id": goal_id},
            name=f"🐦 TEST: {goal_id}"
        )
        logger.info(f"✅ Triggered twitter_flow for goal_id={goal_id}, flow_run_id={flow_run.id}")
        logger.info("🌐 Monitor in Prefect UI: http://localhost:4200")
        return flow_run.id

    except Exception as e:
        log_error_with_trace(logger, "❌ Failed to trigger twitter_flow", e)
        return None

def main():
    parser = argparse.ArgumentParser(description="Insert test goal and trigger twitter_flow via Prefect.")
    parser.add_argument("--goal-id", help="Goal ID to use (default: 959546_44)", default="959546_44")
    args = parser.parse_args()

    logger.info("🎬 FOUND FOOTY - Insert goal and trigger twitter_flow (Prefect deployment)")

    # Step 1: Insert goal into goals_pending
    goal_id = args.goal_id or insert_goal_to_active()
    if not goal_id:
        goal_id = insert_goal_to_active()
    if not goal_id:
        logger.error("❌ Could not insert goal, aborting.")
        return 1

    # Step 2: Trigger twitter_flow deployment
    flow_run_id = asyncio.run(trigger_twitter_flow(goal_id))
    if not flow_run_id:
        logger.error("❌ Could not trigger twitter_flow, aborting.")
        return 2

    logger.info(f"✅ All done! Goal: {goal_id}, Flow Run: {flow_run_id}")
    return 0

if __name__ == "__main__":
    sys.exit(main())