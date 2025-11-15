"""Goal Pipeline - Process goal events and store in MongoDB

Migrated from found_footy/flows/goal_flow.py

This job:
1. Receives goal_events from monitor_fixtures
2. Generates goal_ids
3. Stores goals in MongoDB
4. Triggers Twitter search for new goals

Each monitor run can trigger this job with multiple goals.
Goals are stored in a single "goals" collection with processing_status field.
"""

from dagster import job, op, OpExecutionContext, Config
from typing import List, Dict, Any
from datetime import datetime, timezone

from src.data.mongo_store import FootyMongoStore


class GoalPipelineConfig(Config):
    """Configuration for goal pipeline"""
    fixture_id: int
    goal_events: List[Dict[str, Any]]


@op(
    name="process_goal_events_op",
    description="Process goal events from monitor and store in MongoDB"
)
def process_goal_events_op(context: OpExecutionContext, config: GoalPipelineConfig) -> Dict[str, Any]:
    """
    Process goal events from monitor_fixtures.
    
    For each goal:
    1. Generate goal_id (fixture_id_minute or fixture_id_minute+extra)
    2. Check if goal already exists
    3. Store new goals or update existing ones
    4. (TODO) Schedule Twitter flow with delay for new goals
    
    Based on Prefect's goal_flow.py
    """
    fixture_id = config.fixture_id
    goal_events = config.goal_events
    
    if not goal_events:
        context.log.warning(f"⚠️ No goal events for fixture {fixture_id}")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    # Filter actual goals
    actual_goals = [e for e in goal_events if e.get("type") == "Goal"]
    
    if not actual_goals:
        context.log.info(f"⚽ No actual goals in {len(goal_events)} events")
        return {"status": "no_goals", "fixture_id": fixture_id}
    
    context.log.info(f"⚽ Processing {len(actual_goals)} goal events for fixture {fixture_id}")
    
    store = FootyMongoStore()
    
    # Get existing goal IDs
    existing_goal_ids = set()
    for goal_doc in store.goals.find({"fixture_id": fixture_id}):
        existing_goal_ids.add(goal_doc["_id"])
    
    context.log.info(f"📋 Found {len(existing_goal_ids)} existing goals for fixture {fixture_id}")
    
    new_goals = []
    updated_goals = []
    twitter_flows_scheduled = 0
    
    for goal_event in actual_goals:
        try:
            # Generate goal_id
            time_data = goal_event.get("time", {})
            elapsed = time_data.get("elapsed", 0)
            extra = time_data.get("extra")
            
            if extra is not None and extra > 0:
                goal_id = f"{fixture_id}_{elapsed}+{extra}"
            else:
                goal_id = f"{fixture_id}_{elapsed}"
            
            is_new_goal = goal_id not in existing_goal_ids
            
            # Prepare goal document
            player_data = goal_event.get("player", {})
            team_data = goal_event.get("team", {})
            
            goal_doc = {
                "_id": goal_id,
                "fixture_id": fixture_id,
                "player_name": player_data.get("name", "Unknown"),
                "player_id": player_data.get("id"),
                "team_name": team_data.get("name", "Unknown"),
                "team_id": team_data.get("id"),
                "minute": elapsed,
                "extra_time": extra,
                "goal_type": goal_event.get("detail", "Normal Goal"),
                "assist": goal_event.get("assist", {}).get("name"),
                "processing_status": "discovered",
                "discovered_at": datetime.now(timezone.utc),
                "raw_event": goal_event
            }
            
            if is_new_goal:
                # NEW goal - insert and schedule Twitter
                store.goals.insert_one(goal_doc)
                
                display_minute = f"{elapsed}+{extra}" if extra else str(elapsed)
                context.log.info(f"🆕 NEW GOAL: {team_data.get('name')} - {player_data.get('name')} ({display_minute}') [{goal_id}]")
                
                new_goals.append(goal_id)
                
                # Trigger Twitter search job for new goal
                from src.jobs.twitter_search import twitter_search_job
                
                try:
                    context.log.info(f"🐦 Triggering Twitter search for {goal_id}")
                    result = twitter_search_job.execute_in_process(
                        run_config={
                            "ops": {
                                "search_twitter_for_goal": {
                                    "config": {"goal_id": goal_id}
                                }
                            }
                        },
                        instance=context.instance,
                        tags={
                            "goal_id": goal_id,
                            "fixture_id": str(fixture_id),
                            "triggered_by": "goal_pipeline"
                        }
                    )
                    
                    if result.success:
                        twitter_flows_scheduled += 1
                        context.log.info(f"✅ Twitter search completed for {goal_id}")
                    else:
                        context.log.warning(f"⚠️ Twitter search failed for {goal_id}")
                        
                except Exception as e:
                    context.log.error(f"❌ Failed to trigger Twitter search for {goal_id}: {e}")
                
            else:
                # EXISTING goal - update
                store.goals.replace_one(
                    {"_id": goal_id},
                    goal_doc,
                    upsert=True
                )
                context.log.info(f"🔄 UPDATED GOAL: {goal_id}")
                updated_goals.append(goal_id)
        
        except Exception as e:
            context.log.error(f"❌ Error processing goal event: {e}")
            continue
    
    context.log.info(f"✅ Goal processing complete for fixture {fixture_id}:")
    context.log.info(f"   🆕 New goals: {len(new_goals)}")
    context.log.info(f"   🔄 Updated goals: {len(updated_goals)}")
    context.log.info(f"   🐦 Twitter searches triggered: {twitter_flows_scheduled}")
    
    return {
        "status": "success",
        "fixture_id": fixture_id,
        "new_goals": len(new_goals),
        "updated_goals": len(updated_goals),
        "new_goal_ids": new_goals,
        "updated_goal_ids": updated_goals
    }


@job(
    name="goal_pipeline",
    description="Process goal events and store in MongoDB"
)
def goal_pipeline_job():
    """
    Goal pipeline workflow.
    
    Receives goal events from monitor_fixtures and:
    1. Stores them in MongoDB
    2. Triggers Twitter search for new goals
    3. Twitter search triggers download job
    4. Download job triggers deduplication
    
    Based on Prefect's goal_flow.
    """
    process_goal_events_op()
