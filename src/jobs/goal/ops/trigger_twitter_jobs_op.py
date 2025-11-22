"""Trigger twitter jobs for confirmed goals"""
from typing import Any, Dict

from dagster import OpExecutionContext, op

from src.jobs.twitter.twitter_job import twitter_job


@op(
    name="trigger_twitter_jobs",
    description="Directly execute twitter_job for each confirmed goal",
)
def trigger_twitter_jobs_op(
    context: OpExecutionContext,
    process_result: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Directly execute twitter_job for each confirmed goal.
    
    Args:
        process_result: Result from process_fixture_goals_op
    
    Returns:
        dict: Execution statistics
    """
    confirmed_goal_ids = process_result.get("confirmed_goal_ids", [])
    
    if not confirmed_goal_ids:
        context.log.info("✅ No twitter jobs to trigger")
        return {"status": "success", "triggered_count": 0}
    
    triggered_count = 0
    failed_count = 0
    
    for goal_id in confirmed_goal_ids:
        try:
            context.log.info(f"🐦 Executing twitter_job for goal {goal_id}")
            
            # Execute twitter_job directly with goal_id config
            result = twitter_job.execute_in_process(
                run_config={
                    "ops": {
                        "search_twitter": {
                            "config": {
                                "goal_id": goal_id
                            }
                        }
                    }
                }
            )
            
            if result.success:
                context.log.info(f"✅ twitter_job succeeded for goal {goal_id}")
                triggered_count += 1
            else:
                context.log.error(f"❌ twitter_job failed for goal {goal_id}")
                failed_count += 1
            
        except Exception as e:
            context.log.error(f"❌ Exception executing twitter_job for goal {goal_id}: {e}")
            failed_count += 1
            continue
    
    context.log.info(f"✅ Executed {triggered_count} twitter jobs ({failed_count} failed)")
    
    return {
        "status": "success",
        "triggered_count": triggered_count,
        "failed_count": failed_count,
        "goal_ids": confirmed_goal_ids
    }
