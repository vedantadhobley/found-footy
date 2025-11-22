"""Trigger goal jobs for fixtures with new goals"""
from typing import Any, Dict, List

from dagster import OpExecutionContext, op

from src.jobs.goal.goal_job import goal_job


@op(
    name="trigger_goal_jobs",
    description="Directly execute goal_job for each fixture with goal delta",
)
def trigger_goal_jobs_op(
    context: OpExecutionContext,
    fixtures_with_new_goals: List[Dict[str, Any]]
) -> Dict[str, Any]:
    """
    Directly execute goal_job for each fixture with goal delta.
    
    Args:
        fixtures_with_new_goals: List of fixtures with goal deltas
    
    Returns:
        Dict with execution statistics
    
    Each goal_job will:
    - Validate ALL goals for that fixture
    - Update fixture data in fixtures_active
    - Move fixture to fixtures_completed if ready
    """
    if not fixtures_with_new_goals:
        context.log.info("✅ No goal jobs to trigger")
        return {"status": "success", "triggered_count": 0}
    
    triggered_count = 0
    failed_count = 0
    
    for fixture_data in fixtures_with_new_goals:
        try:
            fixture_id = fixture_data["fixture_id"]
            goal_delta = fixture_data["goal_delta"]
            
            context.log.info(f"🚀 Executing goal_job for fixture {fixture_id} (+{goal_delta} goals)")
            
            # Execute goal_job directly with fixture_id config
            result = goal_job.execute_in_process(
                run_config={
                    "ops": {
                        "process_fixture_goals": {
                            "config": {
                                "fixture_id": fixture_id
                            }
                        }
                    }
                }
            )
            
            if result.success:
                context.log.info(f"✅ goal_job succeeded for fixture {fixture_id}")
                triggered_count += 1
            else:
                context.log.error(f"❌ goal_job failed for fixture {fixture_id}")
                failed_count += 1
            
        except Exception as e:
            context.log.error(f"❌ Exception executing goal_job for fixture {fixture_data.get('fixture_id')}: {e}")
            failed_count += 1
            continue
    
    context.log.info(f"✅ Executed {triggered_count} goal jobs ({failed_count} failed)")
    
    return {
        "status": "success",
        "triggered_count": triggered_count,
        "failed_count": failed_count,
        "total_fixtures": len(fixtures_with_new_goals)
    }
